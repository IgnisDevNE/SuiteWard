package river

import (
	"context"
	"errors"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// The kinds of the GitHub synchronization jobs (docs/contracts/runtime.md, "Synchronization jobs (M1.4)").
const (
	SyncTickKind = "github_sync_tick"
	SyncRepoKind = "github_sync_repo"
)

// defaultSyncInterval is the period of the synchronization tick (D-SYNC).
const defaultSyncInterval = 20 * time.Second

// uniqueWhileActive makes a job unique among the jobs that have not finished
// (River requires available, pending, running and scheduled in a custom list);
// the default would also count completed jobs until River cleans them out.
var uniqueWhileActive = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

type syncTickArgs struct{}

func (syncTickArgs) Kind() string { return SyncTickKind }

type syncTickWorker struct {
	riverqueue.WorkerDefaults[syncTickArgs]
	run   func(ctx context.Context) (Wake, error)
	waker *waker
}

func (w *syncTickWorker) Work(ctx context.Context, _ *riverqueue.Job[syncTickArgs]) error {
	wake, err := w.run(ctx)
	w.waker.wake(ctx, wake)
	return err
}

type syncRepoArgs struct {
	Repo string `json:"repo"`
}

func (syncRepoArgs) Kind() string { return SyncRepoKind }

// InsertOpts coalesces the requests for one repository while one is pending.
func (syncRepoArgs) InsertOpts() riverqueue.InsertOpts {
	return riverqueue.InsertOpts{UniqueOpts: riverqueue.UniqueOpts{ByArgs: true, ByState: uniqueWhileActive}}
}

type syncRepoWorker struct {
	riverqueue.WorkerDefaults[syncRepoArgs]
	run   func(ctx context.Context, repo string) (Wake, error)
	waker *waker
}

func (w *syncRepoWorker) Work(ctx context.Context, job *riverqueue.Job[syncRepoArgs]) error {
	wake, err := w.run(ctx, job.Args.Repo)
	w.waker.wake(ctx, wake)
	return err
}

// addSync registers the synchronization jobs of config in workers and returns
// their periodic jobs (none when config has no synchronization).
func addSync(workers *riverqueue.Workers, config Config, waker *waker) (jobs []*riverqueue.PeriodicJob, err error) {
	if (config.SyncTick == nil) != (config.SyncRepo == nil) {
		return nil, errors.New("river runtime requires both synchronization handlers or neither")
	}
	if config.SyncTick == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &syncTickWorker{run: config.SyncTick, waker: waker}); err != nil {
		return nil, fmt.Errorf("register the synchronization tick: %w", err)
	}
	if err := riverqueue.AddWorkerSafely(workers, &syncRepoWorker{run: config.SyncRepo, waker: waker}); err != nil {
		return nil, fmt.Errorf("register the repository synchronization: %w", err)
	}
	interval := config.SyncInterval
	if interval <= 0 {
		interval = defaultSyncInterval
	}
	tick := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic ticks coalesce while one is unfinished (the kind has a queue of one worker); one that failed is not retried, the next tick is.
			return syncTickArgs{}, &riverqueue.InsertOpts{Queue: SyncTickKind, MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: SyncTickKind, RunOnStart: true})
	return append(jobs, tick), nil
}

// InsertSyncRepo queues a github_sync_repo job for repo. It reports false when
// one for the same repo is still pending, which then covers the request.
func (r *Runtime) InsertSyncRepo(ctx context.Context, repo string) (inserted bool, err error) {
	if !r.sync {
		return false, errors.New("river runtime has no synchronization jobs")
	}
	result, err := r.client.Insert(ctx, syncRepoArgs{Repo: repo}, nil)
	if err != nil {
		return false, fmt.Errorf("insert job %q: %w", SyncRepoKind, err)
	}
	return !result.UniqueSkippedAsDuplicate, nil
}
