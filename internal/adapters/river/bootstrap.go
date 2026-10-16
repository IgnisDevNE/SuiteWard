package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// BootstrapKind is the kind of the periodic github_bootstrap job
// (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
const BootstrapKind = "github_bootstrap"

// defaultBootstrapInterval is the period of the bootstrap job.
const defaultBootstrapInterval = 60 * time.Second

type bootstrapArgs struct{}

func (bootstrapArgs) Kind() string { return BootstrapKind }

type bootstrapWorker struct {
	riverqueue.WorkerDefaults[bootstrapArgs]
	run   func(ctx context.Context) (Wake, error)
	waker *waker
}

func (w *bootstrapWorker) Work(ctx context.Context, _ *riverqueue.Job[bootstrapArgs]) error {
	wake, err := w.run(ctx)
	w.waker.wake(ctx, wake)
	return err
}

// addBootstrap registers the bootstrap job of config in workers and returns its periodic job (none when config has no bootstrap).
func addBootstrap(workers *riverqueue.Workers, config Config, waker *waker) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.Bootstrap == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &bootstrapWorker{run: config.Bootstrap, waker: waker}); err != nil {
		return nil, fmt.Errorf("register the bootstrap: %w", err)
	}
	interval := config.BootstrapInterval
	if interval <= 0 {
		interval = defaultBootstrapInterval
	}
	run := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic runs coalesce while one is unfinished (the kind has a queue of one worker); one that failed is not retried, the next run is.
			return bootstrapArgs{}, &riverqueue.InsertOpts{Queue: BootstrapKind, MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: BootstrapKind, RunOnStart: true})
	return append(jobs, run), nil
}
