package river

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	riverqueue "github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

const (
	// outboxQueue keeps the relay from competing with long jobs for workers.
	outboxQueue = "outbox"
	// relayKind is the job kind of the periodic relay run; no handler or publisher may take it.
	relayKind = "outbox_relay"
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
	Handlers             map[string]Handler           // by job kind, added to the probe handler; the probe kind is reserved
	Publishers           map[string]Publisher         // by message kind, added to the probe publisher; the probe kind is reserved
}

// rescueStuckJobsAfter is when River rescues a job that stayed running.
func (c Config) rescueStuckJobsAfter() time.Duration {
	if c.RescueStuckJobsAfter > 0 {
		return c.RescueStuckJobsAfter
	}
	return c.JobTimeout + time.Minute
}

// dispatcher is the worker of every handler kind: it passes the job's JSON
// arguments to the handler registered for the kind.
type dispatcher struct {
	riverqueue.WorkerDefaults[dynamicArgs]
	handler Handler
}

func (d *dispatcher) Work(ctx context.Context, job *riverqueue.Job[dynamicArgs]) error {
	return d.handler(ctx, job.EncodedArgs)
}

// relayArgs is the periodic job that runs the outbox relay.
type relayArgs struct{}

func (relayArgs) Kind() string { return relayKind }

type relayWorker struct {
	riverqueue.WorkerDefaults[relayArgs]
	relay *Relay
}

func (w *relayWorker) Work(ctx context.Context, _ *riverqueue.Job[relayArgs]) error {
	return w.relay.RunOnce(ctx)
}

// Runtime runs the River workers and the outbox relay.
type Runtime struct {
	client *riverqueue.Client[pgx.Tx]
}

// NewRuntime builds the River client over pool. The relay is a periodic job
// that runs at startup and then every OutboxPollInterval. Relay runs are
// serialized by the single worker of their queue, and concurrent relays of
// several processes are safe because claims use SKIP LOCKED.
func NewRuntime(pool *pgxpool.Pool, store OutboxStore, config Config) (*Runtime, error) {
	if pool == nil || store == nil || config.Logger == nil {
		return nil, errors.New("river runtime requires a connection pool, an outbox store and a logger")
	}
	if config.JobTimeout <= 0 || config.OutboxPollInterval <= 0 {
		return nil, errors.New("river runtime requires a positive job timeout and outbox poll interval")
	}
	for _, reserved := range []string{ProbeKind, relayKind} {
		if _, taken := config.Handlers[reserved]; taken {
			return nil, fmt.Errorf("river runtime: handler kind %q is reserved", reserved)
		}
		if _, taken := config.Publishers[reserved]; taken {
			return nil, fmt.Errorf("river runtime: publisher kind %q is reserved", reserved)
		}
	}
	handlers := map[string]Handler{ProbeKind: ProbeHandler(config.Logger)}
	maps.Copy(handlers, config.Handlers)
	publishers := map[string]Publisher{ProbeKind: ProbePublisher(config.Logger)}
	maps.Copy(publishers, config.Publishers)
	relay, err := NewRelay(RelayConfig{Store: store, Publishers: publishers, MaxAttempts: config.OutboxMaxAttempts, Lease: config.OutboxLease, Now: config.Now, Logger: config.Logger})
	if err != nil {
		return nil, err
	}
	workers := riverqueue.NewWorkers()
	for kind, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("river runtime has no handler function for kind %q", kind)
		}
		// River panics on an invalid kind; the job validation says why before that.
		if err := (governance.Job{Kind: kind, Args: []byte(`{}`)}).Validate(); err != nil {
			return nil, err
		}
		riverqueue.AddWorkerArgs(workers, dynamicArgs{kind: kind}, &dispatcher{handler: handler})
	}
	riverqueue.AddWorker(workers, &relayWorker{relay: relay})
	relayJob := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(config.OutboxPollInterval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			return relayArgs{}, &riverqueue.InsertOpts{Queue: outboxQueue, MaxAttempts: 1}
		},
		&riverqueue.PeriodicJobOpts{ID: "outbox_relay", RunOnStart: true})
	client, err := riverqueue.NewClient(riverpgxv5.New(pool), &riverqueue.Config{
		Logger: config.Logger, Workers: workers, JobTimeout: config.JobTimeout, RescueStuckJobsAfter: config.rescueStuckJobsAfter(), RetryPolicy: config.RetryPolicy,
		Queues:       map[string]riverqueue.QueueConfig{riverqueue.QueueDefault: {MaxWorkers: config.Workers}, outboxQueue: {MaxWorkers: 1}},
		PeriodicJobs: []*riverqueue.PeriodicJob{relayJob},
	})
	if err != nil {
		return nil, fmt.Errorf("create the River client: %w", err)
	}
	return &Runtime{client: client}, nil
}

// Start begins processing jobs and relaying the outbox. It returns once started.
func (r *Runtime) Start(ctx context.Context) error { return r.client.Start(ctx) }

// Stop stops softly: running jobs finish unless ctx ends first.
func (r *Runtime) Stop(ctx context.Context) error { return r.client.Stop(ctx) }

// StopAndCancel stops and cancels the contexts of running jobs.
func (r *Runtime) StopAndCancel(ctx context.Context) error { return r.client.StopAndCancel(ctx) }
