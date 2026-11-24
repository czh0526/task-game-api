package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPTaskIndex is a TaskIndex backed by Elasticsearch or OpenSearch's REST
// API. Both engines speak the same _doc/_search endpoints used here, so the
// only difference is the base URL (and, in production, auth headers).
//
// It is deliberately dependency-free: a plain net/http client keeps the
// binary small and makes the whole index testable with httptest.
type HTTPTaskIndex struct {
	client   *http.Client
	baseURL  string
	index    string
	refresh  bool
	headers  map[string]string
	username string
	password string
	apiKey   string
}

// HTTPIndexConfig configures HTTPTaskIndex.
type HTTPIndexConfig struct {
	URL      string            // e.g. http://localhost:9200
	Index    string            // index name, e.g. taskgame_tasks
	Username string            // optional basic auth
	Password string            // optional basic auth
	APIKey   string            // optional Authorization: ApiKey <value>
	Headers  map[string]string // extra headers (e.g. OpenSearch product headers)
	Refresh  bool              // wait_for refresh on writes (slower, immediate reads)
	Timeout  time.Duration
	Client   *http.Client
}

// NewHTTPTaskIndex validates config and builds an index. It does not contact
// the server; call EnsureIndex (or let the deployment pre-create it) first.
func NewHTTPTaskIndex(cfg HTTPIndexConfig) (*HTTPTaskIndex, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		return nil, fmt.Errorf("search: http index needs URL")
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("search: invalid index URL %q", cfg.URL)
	}
	if strings.TrimSpace(cfg.Index) == "" {
		return nil, fmt.Errorf("search: http index needs Index")
	}
	client := cfg.Client
	if client == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	headers := make(map[string]string, len(cfg.Headers)+1)
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if len(headers) == 0 {
		// Elasticsearch 8+ rejects requests without a product header only
		// when a compatibility layer insists on it; OpenSearch accepts it.
		headers["Accept"] = "application/json"
	}
	return &HTTPTaskIndex{
		client: client, baseURL: base, index: cfg.Index,
		refresh: cfg.Refresh, headers: headers,
		username: cfg.Username, password: cfg.Password, apiKey: cfg.APIKey,
	}, nil
}

// taskDocWire is the JSON shape stored in the index. Keeping it separate from
// TaskDoc avoids leaking transport tags into the in-memory implementation.
type taskDocWire struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Category    string   `json:"category"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
}

func wireOf(doc TaskDoc) taskDocWire {
	kw := doc.Keywords
	if kw == nil {
		kw = []string{}
	}
	return taskDocWire{
		ID: doc.ID, Title: doc.Title, Description: doc.Description, Keywords: kw,
		Category: doc.Category, Kind: doc.Kind, Status: doc.Status, CreatedAt: doc.CreatedAt,
	}
}

func (w taskDocWire) doc() TaskDoc {
	return TaskDoc{
		ID: w.ID, Title: w.Title, Description: w.Description, Keywords: w.Keywords,
		Category: w.Category, Kind: w.Kind, Status: w.Status, CreatedAt: w.CreatedAt,
	}
}

func (h *HTTPTaskIndex) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "ApiKey "+h.apiKey)
	} else if h.username != "" {
		req.SetBasicAuth(h.username, h.password)
	}
	return req, nil
}

// do checks the HTTP status and drains the body so connections can be reused.
func (h *HTTPTaskIndex) do(req *http.Request) ([]byte, error) {
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: http index: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("search: http index %s %s: %s: %s",
			req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// EnsureIndex creates the index with an explicit mapping. It is idempotent:
// an existing index (400 resource_already_exists_exception) is not an error.
func (h *HTTPTaskIndex) EnsureIndex(ctx context.Context) error {
	mapping := map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"id":          map[string]any{"type": "keyword"},
				"title":       map[string]any{"type": "text"},
				"description": map[string]any{"type": "text"},
				"keywords":    map[string]any{"type": "keyword"},
				"category":    map[string]any{"type": "keyword"},
				"kind":        map[string]any{"type": "keyword"},
				"status":      map[string]any{"type": "keyword"},
				"created_at":  map[string]any{"type": "keyword"},
			},
		},
	}
	raw, _ := json.Marshal(mapping)
	req, err := h.newRequest(ctx, http.MethodPut, "/"+url.PathEscape(h.index), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("search: ensure index: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "resource_already_exists_exception") {
		return nil
	}
	return fmt.Errorf("search: ensure index: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (h *HTTPTaskIndex) Upsert(ctx context.Context, doc TaskDoc) error {
	if doc.ID == "" || doc.CreatedAt == "" {
		return fmt.Errorf("search: doc needs id and created_at")
	}
	raw, err := json.Marshal(wireOf(doc))
	if err != nil {
		return err
	}
	path := "/" + url.PathEscape(h.index) + "/_doc/" + url.PathEscape(doc.ID)
	if h.refresh {
		path += "?refresh=wait_for"
	}
	req, err := h.newRequest(ctx, http.MethodPut, path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	_, err = h.do(req)
	return err
}

func (h *HTTPTaskIndex) Delete(ctx context.Context, id string) error {
	// A delete of a missing doc is 404 in ES; treat it as success so replay
	// and out-of-order CDC changes stay idempotent.
	path := "/" + url.PathEscape(h.index) + "/_doc/" + url.PathEscape(id)
	if h.refresh {
		path += "?refresh=wait_for"
	}
	req, err := h.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("search: http index delete: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return fmt.Errorf("search: http index delete: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (h *HTTPTaskIndex) Query(ctx context.Context, q Query) ([]TaskDoc, error) {
	filters := make([]any, 0, 3)
	if q.Keyword != "" {
		filters = append(filters, map[string]any{
			"multi_match": map[string]any{
				"query":  q.Keyword,
				"fields": []string{"title^2", "description", "keywords", "category"},
			},
		})
	}
	if len(q.Categories) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"category": q.Categories}})
	}
	if len(q.Statuses) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"status": q.Statuses}})
	}
	if q.BeforeCursor != "" {
		filters = append(filters, map[string]any{
			"range": map[string]any{"created_at": map[string]any{"lt": q.BeforeCursor}},
		})
	}
	body := map[string]any{
		"query": map[string]any{"bool": map[string]any{"filter": filters}},
		"sort": []any{
			map[string]any{"created_at": "desc"},
			map[string]any{"id": "desc"},
		},
		"size": normalizeLimit(q.Limit),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := h.newRequest(ctx, http.MethodPost, "/"+url.PathEscape(h.index)+"/_search", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	data, err := h.do(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Hits struct {
			Hits []struct {
				Source taskDocWire `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("search: decode index response: %w", err)
	}
	out := make([]TaskDoc, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		if hit.Source.ID != "" {
			out = append(out, hit.Source.doc())
		}
	}
	return out, nil
}

// Count uses the _count endpoint with the same filters as Query (minus the
// cursor, which is pagination rather than a filter).
func (h *HTTPTaskIndex) Count(ctx context.Context, q Query) (int64, error) {
	filters := make([]any, 0, 3)
	if q.Keyword != "" {
		filters = append(filters, map[string]any{
			"multi_match": map[string]any{
				"query":  q.Keyword,
				"fields": []string{"title^2", "description", "keywords", "category"},
			},
		})
	}
	if len(q.Categories) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"category": q.Categories}})
	}
	if len(q.Statuses) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"status": q.Statuses}})
	}
	body := map[string]any{
		"query": map[string]any{"bool": map[string]any{"filter": filters}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := h.newRequest(ctx, http.MethodPost, "/"+url.PathEscape(h.index)+"/_count", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	data, err := h.do(req)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("search: decode count response: %w", err)
	}
	return resp.Count, nil
}

func normalizeLimit(n int) int {
	if n <= 0 || n > 100 {
		return 20
	}
	return n
}

// changeID is a stable, content-addressed key so an unchanged CDC event is
// appended at most once, no matter how many times the write path replays it.
func changeID(c Change) string {
	raw, _ := json.Marshal(struct {
		Kind string  `json:"k"`
		Doc  TaskDoc `json:"d"`
	}{Kind: c.Kind, Doc: c.Doc})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var _ TaskIndex = (*HTTPTaskIndex)(nil)
