package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeES is a minimal in-memory stand-in for the ES/OpenSearch _doc/_search/
// _count/_count endpoints, sufficient to prove the HTTP client sends and
// parses the right shapes.
type fakeES struct {
	mu    sync.Mutex
	docs  map[string]taskDocWire
	index string
}

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.Split(p, "/")
	if len(parts) == 0 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPut && len(parts) == 1 {
		// EnsureIndex
		f.index = parts[0]
		_, _ = w.Write([]byte(`{"acknowledged":true}`))
		return
	}
	if len(parts) >= 3 && parts[1] == "_doc" {
		id := parts[2]
		switch r.Method {
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var doc taskDocWire
			if err := json.Unmarshal(raw, &doc); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.docs[id] = doc
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":"created"}`))
			return
		case http.MethodDelete:
			if _, ok := f.docs[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"result":"not_found"}`))
				return
			}
			delete(f.docs, id)
			_, _ = w.Write([]byte(`{"result":"deleted"}`))
			return
		}
	}
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "_search" {
		items := make([]map[string]any, 0, len(f.docs))
		for _, d := range f.docs {
			items = append(items, map[string]any{"_source": d})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{"hits": items},
		})
		return
	}
	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "_count" {
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(f.docs)})
		return
	}
	http.Error(w, "unhandled", http.StatusNotFound)
}

func TestHTTPTaskIndexCRUD(t *testing.T) {
	es := &fakeES{docs: make(map[string]taskDocWire)}
	srv := httptest.NewServer(es)
	defer srv.Close()

	idx, err := NewHTTPTaskIndex(HTTPIndexConfig{URL: srv.URL, Index: "tasks", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := idx.EnsureIndex(ctx); err != nil {
		t.Fatal(err)
	}
	doc := TaskDoc{
		ID: "tsk_1", Title: "Move piano", Description: "second floor",
		Keywords: []string{"heavy"}, Kind: "errand", Status: "open",
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	if err := idx.Upsert(ctx, doc); err != nil {
		t.Fatal(err)
	}
	docs, err := idx.Query(ctx, Query{Keyword: "piano", Limit: 10})
	if err != nil || len(docs) != 1 || docs[0].ID != "tsk_1" || docs[0].Description != "second floor" {
		t.Fatalf("query: %+v err=%v", docs, err)
	}
	n, err := idx.Count(ctx, Query{})
	if err != nil || n != 1 {
		t.Fatalf("count: %d err=%v", n, err)
	}
	// Deleting a missing doc is idempotent.
	if err := idx.Delete(ctx, "tsk_missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if err := idx.Delete(ctx, "tsk_1"); err != nil {
		t.Fatal(err)
	}
	n, _ = idx.Count(ctx, Query{})
	if n != 0 {
		t.Fatalf("count after delete: %d", n)
	}
}

func TestHTTPTaskIndexRejectsBadConfig(t *testing.T) {
	if _, err := NewHTTPTaskIndex(HTTPIndexConfig{URL: "", Index: "x"}); err == nil {
		t.Fatal("empty URL accepted")
	}
	if _, err := NewHTTPTaskIndex(HTTPIndexConfig{URL: "ftp://x", Index: "x"}); err == nil {
		t.Fatal("bad scheme accepted")
	}
	if _, err := NewHTTPTaskIndex(HTTPIndexConfig{URL: "http://x", Index: ""}); err == nil {
		t.Fatal("empty index accepted")
	}
}

func TestHTTPTaskIndexServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	idx, _ := NewHTTPTaskIndex(HTTPIndexConfig{URL: srv.URL, Index: "x"})
	if err := idx.Upsert(context.Background(), TaskDoc{ID: "a", CreatedAt: "t"}); err == nil {
		t.Fatal("server error ignored")
	}
}
