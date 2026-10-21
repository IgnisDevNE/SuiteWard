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

type syncRepoArgs struct {
	Repo string `json:"repo"`
}

func (syncRepoArgs) Kind() string { return SyncRepoKind }

// InsertOpts coalesces the requests for one repository while one is unfinished, and runs the job on the tick's queue of
// one worker: a sync job and a tick never list the same repository at the same time, so neither commits an older listing
// over a newer one.
func (syncRepoArgs) InsertOpts() riverqueue.InsertOpts {
	return riverqueue.InsertOpts{Queue: SyncTickKind, UniqueOpts: riverqueue.UniqueOpts{ByArgs: true, ByState: uniqueWhileActive}}
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

// addSyncRepo checks that config has both synchronization handlers or neither, and registers the repository
// synchronization worker when it has them; the tick is a periodic job of the table.
func addSyncRepo(workers *riverqueue.Workers, config Config, waker *waker) error {
	if (config.SyncTick == nil) != (config.SyncRepo == nil) {
		return errors.New("river runtime requires both synchronization handlers or neither")
	}
	if config.SyncRepo == nil {
		return nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &syncRepoWorker{run: config.SyncRepo, waker: waker}); err != nil {
		return fmt.Errorf("register the repository synchronization: %w", err)
	}
	return nil
}

// InsertSyncRepo queues a github_sync_repo job for repo. It reports false when one for the same repo is unfinished: a
// waiting one covers the request, a running one leaves it to the next tick.
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
