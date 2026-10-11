package githubsync

import (
	"context"
	"errors"
	"time"
)

// maxInFlight is how many repositories one tick reconciles at once (D-SYNC).
const maxInFlight = 4

// defaultWait is how long a rate limit that names no reset waits: one tick.
const defaultWait = time.Minute

var errNotImplemented = errors.New("githubsync: not implemented")

// Reader lists what one cycle reads from GitHub, in this package's terms; the
// composition adapts the GitHub adapter to it and classifies its errors.
type Reader interface {
	// OpenPulls returns the complete open listing of repo.
	OpenPulls(ctx context.Context, repo string) ([]ObservedPull, error)
	// PullComments returns the complete comment listing of pull number of repo.
	PullComments(ctx context.Context, repo string, number int) ([]ObservedComment, error)
}

// ReadError is a classified failed read. A Reader returns it for every
// failure that is GitHub's or the network's; its text is the fixed kind, never
// text GitHub sent.
type ReadError struct {
	Kind      FailureKind
	Status    int
	WaitUntil time.Time
}

func (e *ReadError) Error() string { return "github read failed: " + string(e.Kind) }

// Reconciler runs the synchronization cycles of the configured repositories.
type Reconciler struct{}

// NewReconciler returns a Reconciler of one installation.
func NewReconciler(reader Reader, store Store, now func() time.Time, installationID int64) (*Reconciler, error) {
	return nil, errNotImplemented
}

// SyncRepo runs one cycle of repo.
func (r *Reconciler) SyncRepo(ctx context.Context, repo string) error { return errNotImplemented }

// Tick runs one cycle of each of repos.
func (r *Reconciler) Tick(ctx context.Context, repos []string, deadline time.Duration) error {
	return errNotImplemented
}
