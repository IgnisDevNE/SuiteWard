package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// IntegrityKind is the kind of the periodic canonical_integrity job
// (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)").
const IntegrityKind = "canonical_integrity"

// defaultIntegrityInterval is the period of the integrity job.
const defaultIntegrityInterval = time.Hour

type integrityArgs struct{}

func (integrityArgs) Kind() string { return IntegrityKind }

type integrityWorker struct {
	riverqueue.WorkerDefaults[integrityArgs]
	run func(ctx context.Context) error
}

func (w *integrityWorker) Work(ctx context.Context, _ *riverqueue.Job[integrityArgs]) error {
	return w.run(ctx)
}

// addIntegrity registers the integrity job of config in workers and returns its periodic job (none when config has no integrity job).
func addIntegrity(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.Integrity == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &integrityWorker{run: config.Integrity}); err != nil {
		return nil, fmt.Errorf("register the integrity job: %w", err)
	}
	interval := config.IntegrityInterval
	if interval <= 0 {
		interval = defaultIntegrityInterval
	}
	run := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic runs coalesce while one is unfinished (the kind has a queue of one worker); one that failed is not retried, the next run is.
			return integrityArgs{}, &riverqueue.InsertOpts{MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: IntegrityKind, RunOnStart: true})
	return append(jobs, run), nil
}
