package river

import (
	"context"
	"errors"
	"time"
)

// The kinds of the GitHub synchronization jobs (docs/contracts/runtime.md, "Synchronization jobs (M1.4)").
const (
	SyncTickKind = "github_sync_tick"
	SyncRepoKind = "github_sync_repo"
)

// defaultSyncInterval is the period of the synchronization tick (D-SYNC).
const defaultSyncInterval = time.Minute

var errNotImplemented = errors.New("river: not implemented")

// InsertSyncRepo queues a github_sync_repo job for repo. It reports false when
// one for the same repo is still pending, which then covers the request.
func (r *Runtime) InsertSyncRepo(ctx context.Context, repo string) (inserted bool, err error) {
	return false, errNotImplemented
}
