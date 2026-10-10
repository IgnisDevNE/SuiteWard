package river

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	riverqueue "github.com/riverqueue/river"
)

// Config configures the River runtime. The zero value of an optional field
// selects the default named in its comment.
type Config struct {
	Logger               *slog.Logger
	Workers              int                          // JOB_WORKERS
	JobTimeout           time.Duration                // JOB_TIMEOUT, per attempt
	RescueStuckJobsAfter time.Duration                // zero means JobTimeout plus one minute
	RetryPolicy          riverqueue.ClientRetryPolicy // nil means River's default
	OutboxMaxAttempts    int                          // OUTBOX_MAX_ATTEMPTS
	OutboxPollInterval   time.Duration                // OUTBOX_POLL_INTERVAL
	OutboxLease          time.Duration                // OUTBOX_LEASE
	Now                  func() time.Time             // nil means time.Now
	Handlers             map[string]Handler           // by job kind, added to the probe handler
	Publishers           map[string]Publisher         // by message kind, added to the probe publisher
}

// Runtime runs the River workers and the outbox relay.
type Runtime struct{}

// NewRuntime builds the River client over pool.
func NewRuntime(pool *pgxpool.Pool, store OutboxStore, config Config) (*Runtime, error) {
	return &Runtime{}, nil
}

// Start begins processing jobs and relaying the outbox. It returns once started.
func (r *Runtime) Start(ctx context.Context) error { return errors.New("not implemented") }

// Stop stops softly: running jobs finish unless ctx ends first.
func (r *Runtime) Stop(ctx context.Context) error { return errors.New("not implemented") }

// StopAndCancel stops and cancels the contexts of running jobs.
func (r *Runtime) StopAndCancel(ctx context.Context) error { return errors.New("not implemented") }
