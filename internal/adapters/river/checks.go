package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// ChecksKind is the kind of the periodic github_checks job, the single writer of check states
// (docs/contracts/runtime.md, "Check job and publisher (M1.6b)").
const ChecksKind = "github_checks"

// defaultChecksInterval is the period of the checks job.
const defaultChecksInterval = 30 * time.Second

type checksArgs struct{}

func (checksArgs) Kind() string { return ChecksKind }

type checksWorker struct {
	riverqueue.WorkerDefaults[checksArgs]
	run func(ctx context.Context) error
}

func (w *checksWorker) Work(ctx context.Context, _ *riverqueue.Job[checksArgs]) error {
	return w.run(ctx)
}

// addChecks registers the checks job of config in workers and returns its periodic job (none when config has no checks).
func addChecks(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.Checks == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &checksWorker{run: config.Checks}); err != nil {
		return nil, fmt.Errorf("register the checks: %w", err)
	}
	interval := config.ChecksInterval
	if interval <= 0 {
		interval = defaultChecksInterval
	}
	run := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic runs coalesce while one is unfinished (the kind has a queue of one worker); one that failed is not retried, the next run is.
			return checksArgs{}, &riverqueue.InsertOpts{Queue: ChecksKind, MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: ChecksKind, RunOnStart: true})
	return append(jobs, run), nil
}
