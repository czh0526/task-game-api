// Command shardctl runs the maintenance steps of a physical shard cutover.
// It is deliberately separate from the API server: no online request path can
// call these operations by accident. See docs/physical-shard-drill.md.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"taskgame/internal/archive"
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
	keys        string
	entity      string
	domains     string
	from        int
	to          int
	skipCleanup bool
	domain      string
	key         string
	reason      string
	owner       string
	ttl         time.Duration
	archiveDir  string
	table       string
	ref         string
	limit       int
	hotWindow   time.Duration
}

func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.driver, "driver", "sqlite", "SQL driver: sqlite or mysql")
	fs.StringVar(&o.control, "control", "", "control-plane DSN (shard_directory / shard_topology)")
	fs.StringVar(&o.dsns, "dsns", "", "comma-separated ordered physical DSNs")
	fs.StringVar(&o.source, "source", "", "source database DSN")
	fs.StringVar(&o.target, "target", "", "target database DSN")
	fs.StringVar(&o.chats, "chats", "", "comma-separated chat ids to move")
	fs.StringVar(&o.keys, "keys", "", "comma-separated aggregate-root keys (alias of -chats)")
	fs.StringVar(&o.entity, "entity", "message", "entity domain: message, user or task")
	fs.StringVar(&o.domains, "domains", "user,task,message", "domains for backfill")
	fs.IntVar(&o.from, "from", 0, "source shard index")
	fs.IntVar(&o.to, "to", 0, "target shard index")
	fs.BoolVar(&o.skipCleanup, "skip-cleanup", false, "cutover only: keep rows on the source shard")
	fs.StringVar(&o.domain, "domain", "user", "fence domain: user, task or message")
	fs.StringVar(&o.key, "key", "", "fence key (a root id, or * for the whole domain)")
	fs.StringVar(&o.reason, "reason", "", "fence reason")
	fs.StringVar(&o.owner, "owner", "shardctl", "fence owner")
	fs.DurationVar(&o.ttl, "ttl", time.Hour, "fence time-to-live")
	fs.StringVar(&o.archiveDir, "blob-dir", "", "cold blob directory for archive-run/restore")
	fs.StringVar(&o.table, "table", "", "table to archive/restore")
	fs.StringVar(&o.ref, "ref", "", "blob ref to restore")
	fs.IntVar(&o.limit, "limit", 500, "max rows per archive batch")
	fs.DurationVar(&o.hotWindow, "hot-window", 0, "override retention hot window")
}

func (o *options) chatIDs() []string {
	if o.keys != "" {
		return split(o.keys)
	}
	return split(o.chats)
}
func (o *options) dsnList() []string { return split(o.dsns) }

// entitySpec returns a spec for user/task, or an empty spec for the legacy
// message path so the existing CLI contract stays valid.
func (o *options) entitySpec() (shardmigrate.EntitySpec, error) {
	if o.entity == "message" {
		return shardmigrate.EntitySpec{}, nil
	}
	spec, ok := shardmigrate.EntitySpecFor(o.entity)
	if !ok {
		return shardmigrate.EntitySpec{}, fmt.Errorf("unknown -entity %q (want message, user or task)", o.entity)
	}
	return spec, nil
}

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
	case "fence-lock":
		err = runFenceLock(ctx, os.Args[2:])
	case "fence-unlock":
		err = runFenceUnlock(ctx, os.Args[2:])
	case "fence-list":
		err = runFenceList(ctx, os.Args[2:])
	case "archive-run":
		err = runArchive(ctx, os.Args[2:])
	case "archive-restore":
		err = runArchiveRestore(ctx, os.Args[2:])
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
  fence-lock    freeze a key (or a whole domain with -key '*') during migration
  fence-unlock  release a frozen key
  fence-list    list active maintenance fences
  archive-run   archive cold rows from a table into blobs, then delete online
  archive-restore restore an archived blob back into a table

plan/copy/verify/cutover/cleanup/rollback all accept -entity=message|user|task
(default message). For user/task use -keys with user ids / task ids; a chat
id list supplied through -chats works too.

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
	values := map[string]bool{
		"control": o.control != "", "dsns": o.dsns != "",
		"source": o.source != "", "target": o.target != "",
		"chats": len(o.chatIDs()) > 0, "keys": len(o.chatIDs()) > 0,
		"blob-dir": o.archiveDir != "", "table": o.table != "", "ref": o.ref != "",
	}
	for _, name := range fields {
		if !values[name] {
			return fmt.Errorf("-%s is required", name)
		}
	}
	return nil
}

func openFence(ctx context.Context, o *options) (*plane.SQLFence, error) {
	return plane.OpenSQLFence(ctx, o.driver, o.control)
}

func runFenceLock(ctx context.Context, args []string) error {
	o, err := parse("fence-lock", args)
	if err != nil {
		return err
	}
	if err := o.require("control", "key"); err != nil {
		return err
	}
	d := plane.Domain(o.domain)
	if !d.Valid() {
		return fmt.Errorf("invalid -domain %q", o.domain)
	}
	f, err := openFence(ctx, o)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Lock(ctx, d, o.key, o.reason, o.owner, o.ttl); err != nil {
		return err
	}
	fmt.Printf("fenced %s/%s for %s (owner=%s, ttl=%s)\n", d, o.key, o.reason, o.owner, o.ttl)
	return nil
}

func runFenceUnlock(ctx context.Context, args []string) error {
	o, err := parse("fence-unlock", args)
	if err != nil {
		return err
	}
	if err := o.require("control", "key"); err != nil {
		return err
	}
	d := plane.Domain(o.domain)
	if !d.Valid() {
		return fmt.Errorf("invalid -domain %q", o.domain)
	}
	f, err := openFence(ctx, o)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Unlock(ctx, d, o.key); err != nil {
		return err
	}
	fmt.Printf("unfenced %s/%s\n", d, o.key)
	return nil
}

func runFenceList(ctx context.Context, args []string) error {
	o, err := parse("fence-list", args)
	if err != nil {
		return err
	}
	if err := o.require("control"); err != nil {
		return err
	}
	f, err := openFence(ctx, o)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := f.List(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no active fences")
		return nil
	}
	for _, r := range rows {
		fmt.Printf("%s/%-20s owner=%-16s expires=%s reason=%s\n",
			r.Domain, r.Key, r.Owner, r.ExpiresAt.Format(time.RFC3339), r.Reason)
	}
	return nil
}

func openArchiveDB(ctx context.Context, o *options, dsn string) (*sql.DB, error) {
	db, err := sql.Open(o.driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func runArchive(ctx context.Context, args []string) error {
	o, err := parse("archive-run", args)
	if err != nil {
		return err
	}
	if err := o.require("source", "blob-dir", "table"); err != nil {
		return err
	}
	blobs, err := archive.NewFileBlobStore(o.archiveDir)
	if err != nil {
		return err
	}
	policies := archive.DefaultPolicies
	if o.hotWindow > 0 {
		policies = []archive.RetentionPolicy{{Table: o.table, TimeColumn: "created_at", HotWindow: o.hotWindow}}
	} else {
		var found *archive.RetentionPolicy
		for i := range archive.DefaultPolicies {
			if archive.DefaultPolicies[i].Table == o.table {
				found = &archive.DefaultPolicies[i]
			}
		}
		if found == nil {
			return fmt.Errorf("no default retention policy for %q; set -hot-window", o.table)
		}
		policies = []archive.RetentionPolicy{*found}
	}
	a := archive.NewArchiver(blobs, policies)
	db, err := openArchiveDB(ctx, o, o.source)
	if err != nil {
		return err
	}
	defer db.Close()
	batches, err := a.Plan(ctx, time.Now())
	if err != nil {
		return err
	}
	res, err := a.ExecuteBatch(ctx, db, o.driver, batches[0], o.limit)
	if err != nil {
		return err
	}
	fmt.Printf("archived %d %s rows into %s (%d bytes, %d deleted online)\n",
		res.Rows, res.Table, res.Ref, res.Bytes, res.Deleted)
	return nil
}

func runArchiveRestore(ctx context.Context, args []string) error {
	o, err := parse("archive-restore", args)
	if err != nil {
		return err
	}
	if err := o.require("source", "blob-dir", "table", "ref"); err != nil {
		return err
	}
	blobs, err := archive.NewFileBlobStore(o.archiveDir)
	if err != nil {
		return err
	}
	a := archive.NewArchiver(blobs, nil)
	db, err := openArchiveDB(ctx, o, o.source)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := a.Restore(ctx, db, o.driver, o.table, o.ref)
	if err != nil {
		return err
	}
	fmt.Printf("restored %d rows into %s from %s\n", n, o.table, o.ref)
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	dsns := o.dsnList()
	if o.from < 0 || o.from >= len(dsns) {
		return fmt.Errorf("-from %d out of range for %d DSNs", o.from, len(dsns))
	}
	if spec.Domain != "" {
		keys, err := shardmigrate.PlanEntity(ctx, o.driver, dsns[o.from], spec, o.chatIDs())
		if err != nil {
			return err
		}
		for _, key := range keys {
			fmt.Printf("%s %s\n", o.entity, key)
		}
		return nil
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	if spec.Domain != "" {
		report, err := shardmigrate.CopyEntity(ctx, o.driver, o.source, o.target, spec, o.to, o.chatIDs())
		if err != nil {
			return err
		}
		for table, n := range report.Copied {
			fmt.Printf("  %s: %d copied, %d already present\n", table, n, report.Skipped[table])
		}
		return nil
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	if spec.Domain != "" {
		if err := shardmigrate.VerifyEntity(ctx, o.driver, o.source, o.target, spec, o.chatIDs()); err != nil {
			return err
		}
	} else if err := shardmigrate.Verify(ctx, o.driver, o.source, o.target, o.to, o.chatIDs()); err != nil {
		return err
	}
	fmt.Printf("verified %s: %s\n", o.entity, strings.Join(o.chatIDs(), ", "))
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	if spec.Domain != "" {
		keys, err := shardmigrate.PlanEntity(ctx, o.driver, o.source, spec, o.chatIDs())
		if err != nil {
			return err
		}
		if err := shardmigrate.CutoverEntity(ctx, o.driver, o.control, spec.Domain, keys, o.from, o.to, o.target); err != nil {
			return err
		}
		fmt.Printf("directory flipped %d %s keys from shard %d to %d\n", len(keys), o.entity, o.from, o.to)
		if o.skipCleanup {
			return nil
		}
		removed, err := shardmigrate.CleanupEntity(ctx, o.driver, o.source, spec, keys)
		if err != nil {
			return err
		}
		fmt.Printf("cleaned %d source entity rows\n", removed)
		return nil
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	if spec.Domain != "" {
		removed, err := shardmigrate.CleanupEntity(ctx, o.driver, o.source, spec, o.chatIDs())
		if err != nil {
			return err
		}
		fmt.Printf("cleaned %d source entity rows (double-written friendships are kept)\n", removed)
		return nil
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
	spec, err := o.entitySpec()
	if err != nil {
		return err
	}
	if spec.Domain != "" {
		if err := shardmigrate.RollbackEntity(ctx, o.driver, o.control, o.source, o.target,
			spec, o.from, o.to, o.chatIDs()); err != nil {
			return err
		}
		fmt.Printf("rolled back %d %s keys from shard %d to %d\n", len(o.chatIDs()), o.entity, o.to, o.from)
		return nil
	}
	if err := shardmigrate.Rollback(ctx, o.driver, o.control, o.source, o.target, o.from, o.to, o.chatIDs()); err != nil {
		return err
	}
	fmt.Printf("rolled back %d chats from shard %d to %d\n", len(o.chatIDs()), o.to, o.from)
	return nil
}
