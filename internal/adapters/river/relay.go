package river

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// OutboxStore is the part of *postgres.Store the relay needs.
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, params postgres.ClaimOutboxParams) ([]postgres.OutboxClaim, error)
	FinishOutbox(ctx context.Context, claim postgres.OutboxClaim, update postgres.OutboxUpdate) (bool, error)
}

// Publisher performs the external effect of one outbox message. Delivery is at
// least once, so it must be idempotent by key.
type Publisher func(ctx context.Context, key string, payload json.RawMessage) error

// RelayConfig configures a Relay.
type RelayConfig struct {
	Store       OutboxStore
	Publishers  map[string]Publisher // by message kind
	MaxAttempts int                  // OUTBOX_MAX_ATTEMPTS
	Lease       time.Duration        // OUTBOX_LEASE
	Now         func() time.Time     // nil means time.Now
	Logger      *slog.Logger
}

// Relay delivers due outbox messages.
type Relay struct{}

// NewRelay returns a Relay after checking its dependencies.
func NewRelay(config RelayConfig) (*Relay, error) { return &Relay{}, nil }

// RunOnce claims and delivers every due message.
func (r *Relay) RunOnce(ctx context.Context) error { return errors.New("not implemented") }

// backoff is the delay before the next attempt after attempts failed attempts.
func backoff(attempts int) time.Duration { return 0 }
