package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// MergesKind is the kind of the periodic github_merges job, the only writer of merge facts
// (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)").
const MergesKind = "github_merges"

// defaultMergesInterval is the period of the merges job.
const defaultMergesInterval = 30 * time.Second

type mergesArgs struct{}

func (mergesArgs) Kind() string { return MergesKind }

type mergesWorker struct {
	riverqueue.WorkerDefaults[mergesArgs]
	run func(ctx context.Context) (Wake, error)
}

func (w *mergesWorker) Work(ctx context.Context, _ *riverqueue.Job[mergesArgs]) error {
	_, err := w.run(ctx)
	return err
}

// addMerges registers the merges job of config in workers and returns its periodic job (none when config has no merges job).
func addMerges(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.Merges == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &mergesWorker{run: config.Merges}); err != nil {
		return nil, fmt.Errorf("register the merges job: %w", err)
	}
	interval := config.MergesInterval
	if interval <= 0 {
		interval = defaultMergesInterval
	}
	run := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// A run does not start while an earlier one runs; one that failed is not retried, the next run is.
			return mergesArgs{}, &riverqueue.InsertOpts{MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: MergesKind, RunOnStart: true})
	return append(jobs, run), nil
}
