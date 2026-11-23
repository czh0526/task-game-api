// Command shardctl runs the maintenance steps of a physical shard cutover.
// It is deliberately separate from the API server: no online request path can
// call these operations by accident. See docs/physical-shard-drill.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"taskgame/internal/plane"
	"taskgame/internal/shardmigrate"
)

type options struct {
	driver      string
	control     string
	dsns        string
	source      string
	target      string
	chats       string
	domains     string
	from        int
	to          int
	skipCleanup bool
}

func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.driver, "driver", "sqlite", "SQL driver: sqlite or mysql")
	fs.StringVar(&o.control, "control", "", "control-plane DSN (shard_directory / shard_topology)")
	fs.StringVar(&o.dsns, "dsns", "", "comma-separated ordered physical DSNs")
	fs.StringVar(&o.source, "source", "", "source database DSN")
	fs.StringVar(&o.target, "target", "", "target database DSN")
	fs.StringVar(&o.chats, "chats", "", "comma-separated chat ids to move")
	fs.StringVar(&o.domains, "domains", "user,task,message", "domains for backfill")
	fs.IntVar(&o.from, "from", 0, "source shard index")
	fs.IntVar(&o.to, "to", 0, "target shard index")
	fs.BoolVar(&o.skipCleanup, "skip-cleanup", false, "cutover only: keep rows on the source shard")
}

func (o *options) chatIDs() []string { return split(o.chats) }
func (o *options) dsnList() []string { return split(o.dsns) }

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(ctx, os.Args[2:])
	case "backfill":
		err = runBackfill(ctx, os.Args[2:])
	case "status":
		err = runStatus(ctx, os.Args[2:])
	case "plan":
		err = runPlan(ctx, os.Args[2:])
	case "copy":
		err = runCopy(ctx, os.Args[2:])
	case "verify":
		err = runVerify(ctx, os.Args[2:])
	case "cutover":
		err = runCutover(ctx, os.Args[2:])
	case "cleanup":
		err = runCleanup(ctx, os.Args[2:])
	case "rollback":
		err = runRollback(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "shardctl: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shardctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `shardctl - physical shard cutover maintenance

Commands:
  init      apply the full business schema to a new physical database
  backfill  pin legacy rows in the control-plane directory
  status    print topology and directory counts
  plan      list chats and directory keys selected for migration
  copy      copy chat rows to a new shard (source rows are kept)
  verify    compare source and target row by row
  cutover   flip directory rows, then delete source rows
  cleanup   delete source rows only
  rollback  copy back, flip directory rows back, delete moved rows

Run "shardctl <command> -h" for that command's flags.
`)
}

func parse(name string, args []string) (*options, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	o := &options{}
	o.bind(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *options) require(fields ...string) error {
	values := map[string]string{
		"control": o.control, "dsns": o.dsns, "source": o.source,
		"target": o.target, "chats": o.chats,
	}
	for _, name := range fields {
		if values[name] == "" {
			return fmt.Errorf("-%s is required", name)
		}
	}
	return nil
}

func runInit(ctx context.Context, args []string) error {
	o, err := parse("init", args)
	if err != nil {
		return err
	}
	if err := o.require("dsns"); err != nil {
		return err
	}
	for _, dsn := range o.dsnList() {
		if err := shardmigrate.EnsureSchema(ctx, o.driver, dsn); err != nil {
			return fmt.Errorf("init %s: %w", dsn, err)
		}
		fmt.Println("schema ready:", dsn)
	}
	return nil
}

func runBackfill(ctx context.Context, args []string) error {
	o, err := parse("backfill", args)
	if err != nil {
		return err
	}
	if err := o.require("control", "dsns"); err != nil {
		return err
	}
	var domains []plane.Domain
	for _, name := range split(o.domains) {
		d := plane.Domain(strings.TrimSpace(name))
		if !d.Valid() {
			return fmt.Errorf("invalid domain %q", name)
		}
		domains = append(domains, d)
	}
	report, err := shardmigrate.Backfill(ctx, o.driver, o.control, o.dsnList(), domains)
	if err != nil {
		return err
	}
	for domain, counts := range report.Counts {
		for shard, n := range counts {
			fmt.Printf("backfill %s shard %d: %d keys\n", domain, shard, n)
		}
	}
	for _, mismatch := range report.Mismatches {
		fmt.Printf("MISMATCH %s\n", mismatch)
	}
	if len(report.Mismatches) > 0 {
		return fmt.Errorf("backfill found %d conflicts; directory was left untouched for those keys", len(report.Mismatches))
	}
	return nil
}

func runStatus(ctx context.Context, args []string) error {
	o, err := parse("status", args)
	if err != nil {
		return err
	}
	if err := o.require("control"); err != nil {
		return err
	}
	dir, err := plane.OpenSQLDirectory(ctx, o.driver, o.control, nil)
	if err != nil {
		return err
	}
	defer dir.Close()
	rows, err := dir.DB().QueryContext(ctx,
		`SELECT domain, shard, dsn FROM shard_topology ORDER BY domain, shard`)
	if err != nil {
		return err
	}
	fmt.Println("topology:")
	for rows.Next() {
		var domain, dsn string
		var shard int
		if err := rows.Scan(&domain, &shard, &dsn); err != nil {
			_ = rows.Close()
			return err
		}
		fmt.Printf("  %-8s shard %d  %s\n", domain, shard, dsn)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = dir.DB().QueryContext(ctx,
		`SELECT domain, shard, COUNT(*) FROM shard_directory GROUP BY domain, shard ORDER BY domain, shard`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Println("directory:")
	for rows.Next() {
		var domain string
		var shard, n int
		if err := rows.Scan(&domain, &shard, &n); err != nil {
			return err
		}
		fmt.Printf("  %-8s shard %d  %d keys\n", domain, shard, n)
	}
	return rows.Err()
}

func runPlan(ctx context.Context, args []string) error {
	o, err := parse("plan", args)
	if err != nil {
		return err
	}
	if err := o.require("dsns", "chats"); err != nil {
		return err
	}
	dsns := o.dsnList()
	if o.from < 0 || o.from >= len(dsns) {
		return fmt.Errorf("-from %d out of range for %d DSNs", o.from, len(dsns))
	}
	plans, err := shardmigrate.PlanChats(ctx, o.driver, dsns[o.from], o.chatIDs())
	if err != nil {
		return err
	}
	for _, p := range plans {
		fmt.Printf("chat %s  key %s  %d messages  %d read cursors\n", p.ID, p.Key, p.Messages, p.Reads)
	}
	return nil
}

func runCopy(ctx context.Context, args []string) error {
	o, err := parse("copy", args)
	if err != nil {
		return err
	}
	if err := o.require("source", "target", "chats"); err != nil {
		return err
	}
	report, err := shardmigrate.Copy(ctx, o.driver, o.source, o.target, o.to, o.chatIDs())
	if err != nil {
		return err
	}
	fmt.Printf("copied %d chats, %d messages, %d read cursors, %d legacy rows\n",
		report.Chats, report.Messages, report.Reads, report.ShardRows)
	return nil
}

func runVerify(ctx context.Context, args []string) error {
	o, err := parse("verify", args)
	if err != nil {
		return err
	}
	if err := o.require("source", "target", "chats"); err != nil {
		return err
	}
	if err := shardmigrate.Verify(ctx, o.driver, o.source, o.target, o.to, o.chatIDs()); err != nil {
		return err
	}
	fmt.Println("verified:", strings.Join(o.chatIDs(), ", "))
	return nil
}

func runCutover(ctx context.Context, args []string) error {
	o, err := parse("cutover", args)
	if err != nil {
		return err
	}
	if err := o.require("control", "target", "source", "chats"); err != nil {
		return err
	}
	plans, err := shardmigrate.PlanChats(ctx, o.driver, o.source, o.chatIDs())
	if err != nil {
		return err
	}
	if err := shardmigrate.Cutover(ctx, o.driver, o.control, plans, o.from, o.to, o.target); err != nil {
		return err
	}
	fmt.Printf("directory flipped %d chats from shard %d to %d\n", len(plans), o.from, o.to)
	if o.skipCleanup {
		return nil
	}
	removed, err := shardmigrate.Cleanup(ctx, o.driver, o.source, o.chatIDs())
	if err != nil {
		return err
	}
	fmt.Printf("cleaned %d source chat rows\n", removed)
	return nil
}

func runCleanup(ctx context.Context, args []string) error {
	o, err := parse("cleanup", args)
	if err != nil {
		return err
	}
	if err := o.require("source", "chats"); err != nil {
		return err
	}
	removed, err := shardmigrate.Cleanup(ctx, o.driver, o.source, o.chatIDs())
	if err != nil {
		return err
	}
	fmt.Printf("cleaned %d source chat rows\n", removed)
	return nil
}

func runRollback(ctx context.Context, args []string) error {
	o, err := parse("rollback", args)
	if err != nil {
		return err
	}
	if err := o.require("control", "source", "target", "chats"); err != nil {
		return err
	}
	if err := shardmigrate.Rollback(ctx, o.driver, o.control, o.source, o.target, o.from, o.to, o.chatIDs()); err != nil {
		return err
	}
	fmt.Printf("rolled back %d chats from shard %d to %d\n", len(o.chatIDs()), o.to, o.from)
	return nil
}
