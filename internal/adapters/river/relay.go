package river

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

const (
	// relayBatch bounds a claim, so that the lease of the last message of a
	// batch is not spent waiting for the earlier ones.
	relayBatch = 10
	// A failed delivery is retried after retryBase, doubling up to retryCap.
	retryBase = 5 * time.Second
	retryCap  = 5 * time.Minute
	// maxErrorBytes bounds the publisher error text that is stored and logged.
	maxErrorBytes = 1024
	// releaseTimeout bounds giving back the untried claims of an aborted run.
	releaseTimeout = 5 * time.Second
)

// OutboxStore is the part of *postgres.Store the relay needs.
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, params postgres.ClaimOutboxParams) (postgres.ClaimOutboxResult, error)
	ReleaseOutbox(ctx context.Context, claim postgres.OutboxClaim, nextAttemptAt time.Time) (bool, error)
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
//
// Two clocks are involved: rows get their first next_attempt_at from the
// database's now(), while every later time (claims, leases, backoff, release)
// comes from the injected Now. They must agree to the accuracy of the lease;
// tests start the injected clock at the database time.
//
// Delivery is at least once. The publisher runs outside any transaction, so a
// crash after the publish and before the outcome is written redelivers the
// message once the lease expires, and a publish that outlasts the lease may
// be delivered twice (its late outcome is then ignored). A publisher must
// therefore be idempotent by key. A claim counts as an attempt even when the
// process dies before trying it, so a poison message that keeps killing its
// relay ends up failed after OUTBOX_MAX_ATTEMPTS claims.
type Relay struct {
	store       OutboxStore
	publishers  map[string]Publisher
	maxAttempts int
	lease       time.Duration
	now         func() time.Time
	logger      *slog.Logger
}

// NewRelay returns a Relay after checking its dependencies.
func NewRelay(config RelayConfig) (*Relay, error) {
	if config.Store == nil || config.Logger == nil {
		return nil, errors.New("outbox relay requires a store and a logger")
	}
	if config.MaxAttempts < 1 || config.Lease <= 0 {
		return nil, errors.New("outbox relay requires at least one attempt and a positive lease")
	}
	for kind, publisher := range config.Publishers {
		if publisher == nil {
			return nil, fmt.Errorf("outbox relay has no publisher function for kind %q", kind)
		}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Relay{store: config.Store, publishers: config.Publishers, maxAttempts: config.MaxAttempts, lease: config.Lease, now: now, logger: config.Logger}, nil
}

// RunOnce claims and delivers every due message. A message whose delivery
// failed is not due again before its backoff has passed, so it ends.
func (r *Relay) RunOnce(ctx context.Context) error {
	for {
		result, err := r.store.ClaimOutbox(ctx, postgres.ClaimOutboxParams{Now: r.now(), Lease: r.lease, MaxAttempts: r.maxAttempts, Limit: relayBatch})
		if err != nil {
			return err
		}
		for _, abandoned := range result.Abandoned {
			r.logger.WarnContext(ctx, "outbox message abandoned: every attempt was claimed without an outcome", "key", abandoned.Key, "kind", abandoned.Kind)
		}
		claims := result.Claims
		for i, claim := range claims {
			if err := r.deliver(ctx, claim); err != nil {
				r.release(ctx, claims[i+1:])
				return err
			}
		}
		if len(claims) < relayBatch {
			return nil
		}
	}
}

// release gives back the claims an aborted run never tried, so that they are
// retried at once without burning an attempt. It is best effort and runs on a
// context the abort did not cancel; a failure only delays them to their lease.
func (r *Relay) release(ctx context.Context, untried []postgres.OutboxClaim) {
	if len(untried) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	for _, claim := range untried {
		if _, err := r.store.ReleaseOutbox(ctx, claim, r.now()); err != nil {
			r.logger.WarnContext(ctx, "outbox claim could not be released; it is retried after its lease", "key", claim.Key, "error", truncate(err.Error()))
		}
	}
}

// publish calls the publisher and turns a panic into an error, so that one
// message cannot abort the batch.
func publish(ctx context.Context, publisher Publisher, claim postgres.OutboxClaim) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("publisher panicked: %s", truncate(fmt.Sprint(recovered)))
		}
	}()
	return publisher(ctx, claim.Key, claim.Payload)
}

// truncate bounds text to maxErrorBytes, cutting at a character boundary.
func truncate(text string) string {
	if len(text) <= maxErrorBytes {
		return text
	}
	cut := maxErrorBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// deliver publishes one claimed message outside any transaction and records
// the outcome, which applies only while the claim is still the latest.
func (r *Relay) deliver(ctx context.Context, claim postgres.OutboxClaim) error {
	var update postgres.OutboxUpdate
	if publisher, known := r.publishers[claim.Kind]; !known {
		update = postgres.OutboxUpdate{State: postgres.OutboxFailed, LastError: fmt.Sprintf("no publisher for kind %q", claim.Kind), FinishedAt: r.now()}
	} else if err := publish(ctx, publisher, claim); err == nil {
		update = postgres.OutboxUpdate{State: postgres.OutboxDelivered, FinishedAt: r.now()}
	} else if claim.Attempts >= r.maxAttempts {
		update = postgres.OutboxUpdate{State: postgres.OutboxFailed, LastError: truncate(err.Error()), FinishedAt: r.now()}
	} else {
		update = postgres.OutboxUpdate{State: postgres.OutboxPending, LastError: truncate(err.Error()), NextAttemptAt: r.now().Add(backoff(claim.Attempts))}
	}
	if update.State != postgres.OutboxDelivered {
		r.logger.WarnContext(ctx, "outbox delivery did not succeed", "key", claim.Key, "kind", claim.Kind, "attempt", claim.Attempts, "state", update.State, "error", update.LastError)
	}
	applied, err := r.store.FinishOutbox(ctx, claim, update)
	if err != nil {
		return err
	}
	if !applied {
		r.logger.WarnContext(ctx, "outbox outcome ignored: stale claim", "key", claim.Key, "attempt", claim.Attempts)
	}
	return nil
}

// backoff is the delay before the next attempt after attempts (at least one)
// failed attempts: exponential and capped.
func backoff(attempts int) time.Duration {
	if attempts > 7 { // retryBase<<6 already exceeds retryCap; larger shifts could overflow
		return retryCap
	}
	return min(retryBase<<(attempts-1), retryCap)
}
