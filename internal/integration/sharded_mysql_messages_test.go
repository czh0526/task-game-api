package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"taskgame/internal/biz"
	"taskgame/internal/config"
	"taskgame/internal/handler"
	"taskgame/internal/middleware"
	"taskgame/internal/svc"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/proc"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
)

// mysqlShardTopology creates one database for the primary store and one per
// shard on the MySQL server named by TASKGAME_MYSQL_DSN. In a real deployment
// the shard DSNs point at different hosts; here they share one server.
func mysqlShardTopology(t *testing.T) (string, []string) {
	t.Helper()
	base := os.Getenv("TASKGAME_MYSQL_DSN")
	if base == "" {
		if os.Getenv("TASKGAME_MYSQL_REQUIRED") == "1" {
			t.Fatal("TASKGAME_MYSQL_DSN is required")
		}
		t.Skip("TASKGAME_MYSQL_DSN not set")
	}
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	adminCfg := *cfg
	adminCfg.DBName = "mysql"
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	names := []string{}
	t.Cleanup(func() {
		for _, name := range names {
			_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		}
		_ = admin.Close()
	})
	makeDB := func() string {
		t.Helper()
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		name := "tg_http_" + hex.EncodeToString(raw[:])
		if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		c := *cfg
		c.DBName = name
		return c.FormatDSN()
	}
	return makeDB(), []string{makeDB(), makeDB()}
}

// TestShardedMySQLMessagesHTTP proves a fully network-backed topology keeps
// the HTTP contract unchanged: MySQL primary + a MySQL DSN list for the shards.
func TestShardedMySQLMessagesHTTP(t *testing.T) {
	httpx.SetErrorHandlerCtx(func(ctx context.Context, err error) (int, any) {
		var be *biz.Error
		if errors.As(err, &be) {
			return be.Status, be.Body()
		}
		logx.WithContext(ctx).Errorf("internal: %v", err)
		return http.StatusInternalServerError, map[string]string{"code": "internal", "message": "internal error"}
	})

	mainDSN, shardDSNs := mysqlShardTopology(t)
	port := freePort(t)
	cfgPath := filepath.Join(t.TempDir(), "taskgame.yaml")
	body := fmt.Sprintf(`
Name: taskgame-api
Host: 127.0.0.1
Port: %d
Mode: test
Timeout: 10000
CpuThreshold: 0
Log:
  Mode: console
  Encoding: plain
  Level: error
  Stat: false
Auth:
  JwtSecret: dev-jwt-secret-change-me-32b
  TokenTTL: 24h
Store:
  Driver: mysql
  DSN: %q
  Shards: 2
  ShardDriver: mysql
  ShardDSNs:
    - %q
    - %q
Escrow:
  InitialBalance: 1000000
  Currency: credits
Webhook:
  Mode: log
  Interval: 1s
  Timeout: 1s
  MaxAttempts: 2
AutoSettle:
  Enabled: false
  Interval: 1s
`, port, mainDSN, shardDSNs[0], shardDSNs[1])
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var c config.Config
	if err := conf.Load(cfgPath, &c); err != nil {
		t.Fatal(err)
	}
	if c.Store.Driver != "mysql" || c.Store.ShardDriver != "mysql" || len(c.Store.ShardDSNs) != 2 {
		t.Fatalf("shard config was not loaded: %+v", c.Store)
	}
	server, err := rest.NewServer(c.RestConf, middleware.LocalDevCORS())
	if err != nil {
		t.Fatal(err)
	}
	sc, err := svc.NewServiceContext(c)
	if err != nil {
		t.Fatal(err)
	}
	handler.RegisterHandlers(server, sc)
	go server.Start()
	t.Cleanup(func() {
		proc.Shutdown()
		_ = sc.Close()
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHealthy(t, base)

	register := func(email string) (string, string) {
		t.Helper()
		st, raw := do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
			"email": email, "password": "password123",
		})
		if st != http.StatusCreated {
			t.Fatalf("register %s: %d %s", email, st, raw)
		}
		var out struct {
			AccessToken string `json:"access_token"`
			User        struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" || out.User.ID == "" {
			t.Fatalf("register body %s", raw)
		}
		return out.AccessToken, out.User.ID
	}
	authHdr := func(token string) []string {
		return []string{"Authorization", "Bearer " + token}
	}
	tokA, _ := register("alice@mysqlshard.test")
	tokB, uidB := register("bob@mysqlshard.test")
	tokC, uidC := register("carol@mysqlshard.test")

	createChat := func(peer string) string {
		t.Helper()
		st, raw := do(t, http.MethodPost, base+"/v1/chats", "",
			map[string]any{"type": "direct", "user_id": peer}, authHdr(tokA)...)
		if st != http.StatusCreated && st != http.StatusOK {
			t.Fatalf("create chat: %d %s", st, raw)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
			t.Fatalf("chat body %s", raw)
		}
		return out.ID
	}
	c1 := createChat(uidB)
	c2 := createChat(uidC)

	main, err := sql.Open("mysql", mainDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	readDir := func() map[string]int {
		t.Helper()
		rows, err := main.Query(`SELECT chat_id, shard FROM chat_shards`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var id string
			var s int
			if err := rows.Scan(&id, &s); err != nil {
				t.Fatal(err)
			}
			out[id] = s
		}
		return out
	}
	directory := readDir()
	secondPeerToken := tokC
	for extra := 0; directory[c1] == directory[c2] && extra < 16; extra++ {
		newToken, uid := register(fmt.Sprintf("peer%d@mysqlshard.test", extra))
		c2 = createChat(uid)
		secondPeerToken = newToken
		directory = readDir()
	}
	if directory[c1] == directory[c2] {
		t.Fatalf("could not find a cross-shard pair: %v", directory)
	}

	post := func(chatID, token, text, idemKey string) string {
		t.Helper()
		hdrs := authHdr(token)
		if idemKey != "" {
			hdrs = append(hdrs, "Idempotency-Key", idemKey)
		}
		st, raw := do(t, http.MethodPost, base+"/v1/chats/"+chatID+"/messages", "",
			map[string]any{"type": "text", "text": text}, hdrs...)
		if st != http.StatusCreated && st != http.StatusOK {
			t.Fatalf("post %q: %d %s", text, st, raw)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
			t.Fatalf("message body %s", raw)
		}
		return out.ID
	}
	type msgItem struct {
		Text string `json:"text"`
	}
	list := func(chatID, token string) []msgItem {
		t.Helper()
		st, raw := do(t, http.MethodGet, base+"/v1/chats/"+chatID+"/messages", "", nil, authHdr(token)...)
		if st != http.StatusOK {
			t.Fatalf("list %s: %d %s", chatID, st, raw)
		}
		var out struct {
			Items []msgItem `json:"items"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("list body %s", raw)
		}
		return out.Items
	}

	post(c1, tokA, "hello from A", "")
	m2 := post(c1, tokB, "hi from B", "")
	items := list(c1, tokB)
	if len(items) != 2 || items[0].Text != "hello from A" || items[1].Text != "hi from B" {
		t.Fatalf("unexpected thread %+v", items)
	}
	st, raw := do(t, http.MethodPost, base+"/v1/chats/"+c1+"/read", "",
		map[string]any{"message_id": m2}, authHdr(tokB)...)
	if st != http.StatusOK {
		t.Fatalf("read by message id: %d %s", st, raw)
	}
	var read struct {
		UnreadCount int64 `json:"unread_count"`
	}
	if err := json.Unmarshal(raw, &read); err != nil || read.UnreadCount != 0 {
		t.Fatalf("read body %s", raw)
	}
	post(c1, tokA, "third", "")
	st, raw = do(t, http.MethodPost, base+"/v1/chats/"+c1+"/read", "", map[string]any{}, authHdr(tokB)...)
	if st != http.StatusOK {
		t.Fatalf("read without message id: %d %s", st, raw)
	}
	if err := json.Unmarshal(raw, &read); err != nil || read.UnreadCount != 0 {
		t.Fatalf("read body %s", raw)
	}
	before := len(list(c1, tokB))
	post(c1, tokA, "idempotent", "mysql-shard-http-key-1")
	post(c1, tokA, "idempotent", "mysql-shard-http-key-1")
	if after := len(list(c1, tokB)); after != before+1 {
		t.Fatalf("idempotent replay duplicated: %d -> %d", before, after)
	}
	post(c2, secondPeerToken, "on the other shard", "")
	if got := list(c2, tokA); len(got) != 1 || got[0].Text != "on the other shard" {
		t.Fatalf("second chat thread %+v", got)
	}

	// Physical layout: the primary MySQL database has no message bodies, the
	// outbox is fully projected, and each chat lives in exactly one shard DB.
	var primary int
	if err := main.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if primary != 0 {
		t.Fatalf("primary messages table should be empty, got %d", primary)
	}
	var outbox, projected int
	if err := main.QueryRow(`SELECT COUNT(*), COALESCE(SUM(projected),0) FROM message_outbox`).Scan(&outbox, &projected); err != nil {
		t.Fatal(err)
	}
	if outbox != 5 || projected != 5 {
		t.Fatalf("outbox rows=%d projected=%d", outbox, projected)
	}
	directory = readDir()
	s1, s2 := directory[c1], directory[c2]
	if s1 == s2 {
		t.Fatalf("chats must land on different shards: %v", directory)
	}
	counts := map[int]map[string]int{}
	for i, dsn := range shardDSNs {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal(err)
		}
		counts[i] = map[string]int{}
		r, err := db.Query(`SELECT chat_id, COUNT(*) FROM messages GROUP BY chat_id`)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		for r.Next() {
			var id string
			var n int
			if err := r.Scan(&id, &n); err != nil {
				_ = r.Close()
				_ = db.Close()
				t.Fatal(err)
			}
			counts[i][id] = n
		}
		_ = r.Close()
		_ = db.Close()
	}
	if counts[s1][c1] != 4 || counts[s1][c2] != 0 || counts[s2][c2] != 1 || counts[s2][c1] != 0 {
		t.Fatalf("shard isolation broken: %v", counts)
	}
}
