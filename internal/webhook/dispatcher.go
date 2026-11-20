// Package webhook signs and delivers queued events.
// Mode "log" records the signature and does not POST. Mode "post" is best-effort HTTP.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"

	"github.com/zeromicro/go-zero/core/logx"
)

const (
	HeaderSignature = "X-Signature"
	HeaderEventID   = "X-Event-Id"
)

// Config controls the delivery worker.
type Config struct {
	Mode        string
	Timeout     time.Duration
	MaxAttempts int
}

// Dispatcher drains pending deliveries.
type Dispatcher struct {
	store  store.Store
	cfg    Config
	client *http.Client
}

// NewDispatcher builds a worker. Mode defaults to post.
func NewDispatcher(st store.Store, cfg Config) *Dispatcher {
	if cfg.Mode == "" {
		cfg.Mode = "post"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	return &Dispatcher{
		store: st,
		cfg:   cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Sign is hex HMAC-SHA256 of the raw body using the webhook secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// DeliverPending claims one batch, then POSTs outside the claim.
// A second worker does not deliver a row whose lease is still held.
// Receivers should still dedupe on X-Event-Id: a crash after POST and before
// Finish, or a POST that outlives the lease, can deliver the same event again.
func (d *Dispatcher) DeliverPending(ctx context.Context) error {
	now := time.Now()
	lease := d.cfg.Timeout
	if lease < 30*time.Second {
		lease = 30 * time.Second
	}
	rows, err := d.store.Deliveries().ClaimPending(ctx, d.cfg.MaxAttempts, 20, now, now.Add(lease))
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.deliverOne(ctx, row)
	}
	return nil
}

func (d *Dispatcher) deliverOne(ctx context.Context, row model.Delivery) {
	body := []byte(row.Body)
	sig := Sign(row.Secret, body)
	if d.cfg.Mode == "log" {
		logx.WithContext(ctx).Infof("webhook %s event %s type %s signature %s (log mode)", row.WebhookID, row.EventID, row.EventType, sig)
		_ = d.store.Deliveries().Finish(ctx, row.ID, row.ClaimToken, model.DeliveryDelivered, "")
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, row.URL, bytes.NewReader(body))
	if err != nil {
		d.finish(ctx, row, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "taskgame/0.1")
	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderEventID, row.EventID)
	resp, err := d.client.Do(req)
	if err != nil {
		d.finish(ctx, row, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logx.WithContext(ctx).Infof("webhook %s event %s delivered status %d", row.WebhookID, row.EventID, resp.StatusCode)
		_ = d.store.Deliveries().Finish(ctx, row.ID, row.ClaimToken, model.DeliveryDelivered, "")
		return
	}
	d.finish(ctx, row, fmt.Sprintf("status %d", resp.StatusCode))
}

func (d *Dispatcher) finish(ctx context.Context, row model.Delivery, message string) {
	if len(message) > 500 {
		message = message[:500]
	}
	status := model.DeliveryPending
	if row.Attempts >= d.cfg.MaxAttempts {
		status = model.DeliveryFailed
	}
	logx.WithContext(ctx).Errorf("webhook %s event %s attempt %d: %s", row.WebhookID, row.EventID, row.Attempts, message)
	_ = d.store.Deliveries().Finish(ctx, row.ID, row.ClaimToken, status, message)
}
