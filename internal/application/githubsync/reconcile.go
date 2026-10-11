package githubsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// maxInFlight is how many repositories one tick reconciles at once (D-SYNC).
const maxInFlight = 4

// defaultWait is how long a rate limit that names no reset waits: one tick.
const defaultWait = time.Minute

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
	WaitUntil time.Time // when to try again, for rate_limited and budget_exhausted
}

func (e *ReadError) Error() string { return "github read failed: " + string(e.Kind) }

// Reconciler runs the synchronization cycles of one installation's repositories.
type Reconciler struct {
	reader         Reader
	store          Store
	now            func() time.Time
	installationID int64
}

// NewReconciler returns a Reconciler that stamps its records with now.
func NewReconciler(reader Reader, store Store, now func() time.Time, installationID int64) (*Reconciler, error) {
	if reader == nil || store == nil || now == nil {
		return nil, errors.New("githubsync reconciler requires a reader, a store and a clock")
	}
	if installationID <= 0 {
		return nil, errors.New("githubsync reconciler requires a positive installation id")
	}
	return &Reconciler{reader: reader, store: store, now: now, installationID: installationID}, nil
}

// SyncRepo runs one cycle of repo: it lists the open pull requests and the
// comments of each in full, and records them, or records the classified
// failure. It returns without calling GitHub while the repository's stored
// wait_until has not passed. A failed cycle is not an error: it is recorded.
// An error is returned for what no cycle record describes: a canceled context,
// an unclassified read error, a store failure.
func (r *Reconciler) SyncRepo(ctx context.Context, repo string) error {
	state, found, err := r.store.Repository(ctx, repo)
	if err != nil {
		return fmt.Errorf("read the state of %s: %w", repo, err)
	}
	if found && state.WaitUntil.After(r.now()) {
		return nil
	}
	cycle, err := r.list(ctx, repo)
	if err != nil {
		return r.fail(ctx, repo, err)
	}
	cycle.At = r.now()
	if err := r.store.RecordCycle(ctx, cycle); err != nil {
		return fmt.Errorf("record the cycle of %s: %w", repo, err)
	}
	return nil
}

// list reads every listing of one cycle.
func (r *Reconciler) list(ctx context.Context, repo string) (Cycle, error) {
	pulls, err := r.reader.OpenPulls(ctx, repo)
	if err != nil {
		return Cycle{}, err
	}
	cycle := Cycle{Repo: repo, InstallationID: r.installationID, OpenPulls: pulls, Comments: make(map[int][]ObservedComment, len(pulls))}
	for _, pull := range pulls {
		if cycle.Comments[pull.Number], err = r.reader.PullComments(ctx, repo, pull.Number); err != nil {
			return Cycle{}, err
		}
	}
	return cycle, nil
}

// fail records the classified read failure; any other error is returned.
func (r *Reconciler) fail(ctx context.Context, repo string, readErr error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("cycle of %s: %w", repo, ctx.Err())
	}
	var classified *ReadError
	if !errors.As(readErr, &classified) {
		return fmt.Errorf("cycle of %s: %w", repo, readErr)
	}
	now := r.now()
	wait := classified.WaitUntil
	if wait.IsZero() && classified.Kind == FailureRateLimited {
		wait = now.Add(defaultWait)
	}
	failure := Failure{Repo: repo, InstallationID: r.installationID, At: now, Kind: classified.Kind, Status: classified.Status, WaitUntil: wait}
	if err := r.store.RecordFailure(ctx, failure); err != nil {
		return fmt.Errorf("record the failure of %s: %w", repo, err)
	}
	return nil
}

// Tick runs one cycle of each of repos, at most four at once. No repository is
// started once deadline has passed and a cycle that has not finished by then
// records nothing, so only finished cycles are recorded; the repositories not
// reached wait for the next tick. A deadline or a canceled ctx is an expected
// end, not an error; the errors of the other cycles are joined.
func (r *Reconciler) Tick(ctx context.Context, repos []string, deadline time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		errs  []error
		slots = make(chan struct{}, maxInFlight)
	)
	for _, repo := range repos {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil { // a free slot and an ended context are both ready: the context wins
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := r.SyncRepo(ctx, repo); err != nil && ctx.Err() == nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
