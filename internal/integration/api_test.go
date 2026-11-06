package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/config"
	"taskgame/internal/handler"
	"taskgame/internal/middleware"
	"taskgame/internal/svc"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/proc"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func TestExclusiveHTTPHappyPath(t *testing.T) {
	httpx.SetErrorHandlerCtx(func(ctx context.Context, err error) (int, any) {
		var be *biz.Error
		if errors.As(err, &be) {
			return be.Status, be.Body()
		}
		logx.WithContext(ctx).Errorf("internal: %v", err)
		return http.StatusInternalServerError, map[string]string{"code": "internal", "message": "internal error"}
	})

	dir := t.TempDir()
	port := freePort(t)
	dsn := "file:" + filepath.Join(dir, "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	cfgPath := filepath.Join(dir, "taskgame.yaml")
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
  ApiKeys:
    - Key: dev-publisher-key
      UserId: usr_publisher
    - Key: dev-executor-key
      UserId: usr_executor
Store:
  Driver: sqlite
  DSN: %q
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
`, port, dsn)
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var c config.Config
	if err := conf.Load(cfgPath, &c); err != nil {
		t.Fatal(err)
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
	assertLocalDevCORS(t, base)

	if status, _ := do(t, http.MethodGet, base+"/v1/tasks", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", status)
	}

	deadline := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	createBody := map[string]any{
		"kind":        "exclusive",
		"title":       "Translate release notes",
		"description": "Translate the v0.1 notes from Chinese to English.",
		"payment":     map[string]any{"mode": "one_shot", "amount": 500},
		"acceptance": map[string]any{
			"deadline": deadline, "quality": "Complete translation", "review_hours": 72,
		},
		"assignee_policy": map[string]any{"type": "open"},
	}
	status, raw := do(t, http.MethodPost, base+"/v1/tasks", "dev-publisher-key", createBody, "Idempotency-Key", "http-create")
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, raw)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" || created.Status != "draft" {
		t.Fatalf("create body %s", raw)
	}
	status, raw = do(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/fund", "dev-publisher-key", nil)
	if status != http.StatusOK {
		t.Fatalf("fund %d %s", status, raw)
	}

	tok, err := auth.SignHS256(c.Auth.JwtSecret, "usr_executor", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	status, raw = doBearer(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/accept", tok, map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("accept %d %s", status, raw)
	}
	status, raw = doBearer(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/submit", tok, map[string]any{
		"payload": map[string]any{"body": "translated"}, "note": "done",
	})
	if status != http.StatusOK {
		t.Fatalf("submit %d %s", status, raw)
	}
	status, raw = do(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/confirm", "dev-publisher-key", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("confirm %d %s", status, raw)
	}
	status, raw = do(t, http.MethodGet, base+"/v1/tasks/"+created.ID, "dev-publisher-key", nil)
	if status != http.StatusOK {
		t.Fatalf("get %d %s", status, raw)
	}
	var got struct {
		Status string `json:"status"`
		Escrow struct {
			Held int64 `json:"held"`
		} `json:"escrow"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "settled" || got.Escrow.Held != 0 {
		t.Fatalf("final %+v", got)
	}
	status, raw = do(t, http.MethodGet, base+"/v1/users/usr_executor/scores", "dev-publisher-key", nil)
	if status != http.StatusOK {
		t.Fatalf("scores %d %s", status, raw)
	}

	exercisePasswordAuth(t, base, c.Auth.JwtSecret)
}

func assertLocalDevCORS(t *testing.T, base string) {
	t.Helper()
	preflight, err := http.NewRequest(http.MethodOptions, base+"/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "http://localhost:5173")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "content-type,x-api-key,idempotency-key")
	resp, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("preflight allow-origin %q", got)
	}
	for _, path := range []string{"/v1/register", "/v1/login", "/v1/me"} {
		req, err := http.NewRequest(http.MethodOptions, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,authorization")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight %s status %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
			t.Fatalf("preflight %s allow-origin %q", path, got)
		}
	}
	allow := strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers"))
	for _, name := range []string{"x-api-key", "idempotency-key", "authorization", "content-type"} {
		if !strings.Contains(allow, name) {
			t.Fatalf("preflight allow-headers %q missing %s", allow, name)
		}
	}

	health, err := http.NewRequest(http.MethodGet, base+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	health.Header.Set("Origin", "http://127.0.0.1:5174")
	resp, err = http.DefaultClient.Do(health)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health with origin %d %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5174" {
		t.Fatalf("health allow-origin %q", got)
	}

	foreign, err := http.NewRequest(http.MethodGet, base+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Header.Set("Origin", "https://evil.example")
	resp, err = http.DefaultClient.Do(foreign)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("foreign origin health %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was allowed: %q", got)
	}
}

func exercisePasswordAuth(t *testing.T, base, secret string) {
	t.Helper()
	status, raw := do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
		"email": "not-an-email", "password": "password123",
	})
	if status != http.StatusBadRequest || !strings.Contains(string(raw), `"invalid_argument"`) {
		t.Fatalf("bad email %d %s", status, raw)
	}
	status, raw = do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
		"email": "pub@example.com", "password": "short",
	})
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "at least 8") {
		t.Fatalf("short password %d %s", status, raw)
	}

	status, raw = do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
		"email": "Publisher@Example.com", "password": "password123",
	})
	if status != http.StatusCreated {
		t.Fatalf("register %d %s", status, raw)
	}
	if strings.Contains(string(raw), "password123") || strings.Contains(string(raw), "password_hash") {
		t.Fatalf("register response leaked a secret: %s", raw)
	}
	var pub struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		User        struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &pub); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub.User.ID, "usr_") || pub.User.Email != "publisher@example.com" || pub.ExpiresIn != 24*60*60 {
		t.Fatalf("register body %s", raw)
	}
	sub, err := auth.ParseHS256(secret, pub.AccessToken, time.Now().Add(time.Minute))
	if err != nil || sub != pub.User.ID {
		t.Fatalf("register token sub %q err %v", sub, err)
	}

	status, raw = do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
		"email": "publisher@example.com", "password": "password123",
	})
	if status != http.StatusConflict || !strings.Contains(string(raw), `"conflict"`) {
		t.Fatalf("duplicate %d %s", status, raw)
	}
	status, raw = do(t, http.MethodPost, base+"/v1/login", "", map[string]any{
		"email": "publisher@example.com", "password": "wrong-password",
	})
	if status != http.StatusUnauthorized || !strings.Contains(string(raw), `"unauthorized"`) {
		t.Fatalf("bad login %d %s", status, raw)
	}
	status, raw = do(t, http.MethodPost, base+"/v1/login", "", map[string]any{
		"email": "missing@example.com", "password": "password123",
	})
	if status != http.StatusUnauthorized || !strings.Contains(string(raw), "invalid email or password") {
		t.Fatalf("missing login %d %s", status, raw)
	}

	status, raw = doBearer(t, http.MethodGet, base+"/v1/me", pub.AccessToken, nil)
	if status != http.StatusOK {
		t.Fatalf("me %d %s", status, raw)
	}
	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &me); err != nil || me.ID != pub.User.ID || me.Email != pub.User.Email {
		t.Fatalf("me body %s", raw)
	}
	if status, _ := do(t, http.MethodGet, base+"/v1/me", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("me unauthenticated %d", status)
	}
	status, raw = do(t, http.MethodGet, base+"/v1/me", "dev-publisher-key", nil)
	if status != http.StatusOK {
		t.Fatalf("me api key %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &me); err != nil || me.ID != "usr_publisher" || me.Email != "" {
		t.Fatalf("dev me %s", raw)
	}

	status, raw = do(t, http.MethodPost, base+"/v1/register", "", map[string]any{
		"email": "executor@example.com", "password": "password123",
	})
	if status != http.StatusCreated {
		t.Fatalf("executor register %d %s", status, raw)
	}
	var exe struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &exe); err != nil || exe.User.ID == "" || exe.User.ID == pub.User.ID {
		t.Fatalf("executor body %s", raw)
	}

	deadline := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	status, raw = doBearer(t, http.MethodPost, base+"/v1/tasks", pub.AccessToken, map[string]any{
		"kind":            "exclusive",
		"title":           "Password account task",
		"description":     "Created with a register JWT.",
		"payment":         map[string]any{"mode": "one_shot", "amount": 500},
		"acceptance":      map[string]any{"deadline": deadline, "quality": "Done", "review_hours": 72},
		"assignee_policy": map[string]any{"type": "open"},
	})
	if status != http.StatusCreated {
		t.Fatalf("jwt create %d %s", status, raw)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		t.Fatalf("jwt create body %s", raw)
	}
	status, raw = doBearer(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/fund", pub.AccessToken, nil)
	if status != http.StatusOK {
		t.Fatalf("jwt fund %d %s", status, raw)
	}
	status, raw = doBearer(t, http.MethodPost, base+"/v1/tasks/"+created.ID+"/accept", exe.AccessToken, map[string]any{})
	if status != http.StatusOK || !strings.Contains(string(raw), `"active"`) {
		t.Fatalf("jwt accept %d %s", status, raw)
	}
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server did not become healthy")
}

func do(t *testing.T, method, url, apiKey string, body any, extra ...string) (int, []byte) {
	t.Helper()
	return call(t, method, url, body, func(req *http.Request) {
		if apiKey != "" {
			req.Header.Set("X-Api-Key", apiKey)
		}
		for i := 0; i+1 < len(extra); i += 2 {
			req.Header.Set(extra[i], extra[i+1])
		}
	})
}

func doBearer(t *testing.T, method, url, token string, body any) (int, []byte) {
	t.Helper()
	return call(t, method, url, body, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	})
}

func call(t *testing.T, method, url string, body any, hdr func(*http.Request)) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hdr(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
