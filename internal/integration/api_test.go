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
