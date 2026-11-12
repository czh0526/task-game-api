package config

import (
	"time"

	"github.com/zeromicro/go-zero/rest"
)

// Config is the process config loaded from etc/taskgame.yaml.
type Config struct {
	rest.RestConf
	Auth       AuthConf
	Store      StoreConf
	Escrow     EscrowConf
	Webhook    WebhookConf
	AutoSettle AutoSettleConf
}

// AuthConf is HS256 JWT verification plus static X-Api-Key mappings.
type AuthConf struct {
	JwtSecret string
	ApiKeys   []ApiKeyConf `json:",optional"`
	// TokenTTL is the lifetime of JWTs from register and login.
	// Zero means 24h. Claims match `make token`: sub, iat, exp, HS256.
	TokenTTL time.Duration `json:",optional"`
}

// ApiKeyConf maps one X-Api-Key header value to a user id.
type ApiKeyConf struct {
	Key    string
	UserId string
}

// StoreConf selects the Store implementation. v0.1 ships sqlite only.
type StoreConf struct {
	Driver string `json:",default=sqlite,options=sqlite"`
	DSN    string
}

// EscrowConf is the simulated credit ledger. No real payment provider is called.
type EscrowConf struct {
	InitialBalance int64  `json:",default=1000000"`
	Currency       string `json:",default=credits"`
	// ArbiterUserIds may call POST /tasks/{taskId}/arbitrate. Publisher and executor are not arbiters.
	ArbiterUserIds []string `json:",optional"`
}

// WebhookConf controls the outbound delivery worker.
// Mode log records the signature and does not POST. Mode post delivers HTTP.
type WebhookConf struct {
	Mode        string        `json:",default=log,options=log|post"`
	Interval    time.Duration `json:",default=2s"`
	Timeout     time.Duration `json:",default=5s"`
	MaxAttempts int           `json:",default=3"`
}

// AutoSettleConf runs review auto-settle and the workflow timeouts.
// Zero timeout durations mean 72h, the same window as review_hours.
type AutoSettleConf struct {
	Enabled           bool          `json:",default=true"`
	Interval          time.Duration `json:",default=30s"`
	OpenTimeout       time.Duration `json:",default=72h"`
	StartTimeout      time.Duration `json:",default=72h"`
	SeatTimeout       time.Duration `json:",default=72h"`
	WinnerSelectHours int           `json:",default=72"`
}
