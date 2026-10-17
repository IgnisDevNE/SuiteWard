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

// maxDateAhead is how far ahead of the local clock a response's Date may be. A broken or hostile Date would otherwise
// push the repository's horizon ahead of real time; the local clock is only this bound and never enters the horizon.
const maxDateAhead = 5 * time.Minute

// Reader lists what one cycle reads from GitHub, in this package's terms; the
// composition adapts the GitHub adapter to it and classifies its errors.
type Reader interface {
	// OpenPulls returns the complete open listing of repo and what reading it cost.
	OpenPulls(ctx context.Context, repo string) ([]ObservedPull, ReadInfo, error)
	// PullComments returns the complete comment listing of pull number of repo and what reading it cost.
	PullComments(ctx context.Context, repo string, number int) ([]ObservedComment, ReadInfo, error)
}

// ReadInfo is what one listing cost and saw: GitHub's Date of its first page (zero when missing or unparsable), the
// requests it sent, and whether any page was not a 304. A failed read reports the requests it sent too.
type ReadInfo struct {
	Date     time.Time
	Requests int
	Changed  bool
}

// FirstObserved is a comment a cycle recorded for the first time.
type FirstObserved struct {
	Repo              string
	CommentID         int64
	CreatedAt         time.Time // GitHub's
	StartedAt         time.Time // the recording cycle's start
	PreviousStartedAt time.Time // the repository's stored CycleStartedAt before this cycle; zero when it had none
}

// TickResult is what SyncRepo or Tick did: the requests it made, the cycles it recorded, whether a recorded cycle
// changed, and the comments it recorded for the first time (a Tick's in the order of its repositories, then by comment id).
type TickResult struct {
	Requests      int
	Recorded      int
	Changed       bool
	FirstObserved []FirstObserved
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
func (r *Reconciler) SyncRepo(ctx context.Context, repo string) (TickResult, error) {
	state, found, err := r.store.Repository(ctx, repo)
	if err != nil {
		return TickResult{}, fmt.Errorf("read the state of %s: %w", repo, err)
	}
	if found && state.WaitUntil.After(r.now()) {
		return TickResult{}, nil
	}
	cycle, err := r.list(ctx, repo)
	if err != nil {
		return TickResult{Requests: cycle.Requests}, r.fail(ctx, repo, err)
	}
	cycle.At = r.now()
	ids, err := r.store.RecordCycle(ctx, cycle)
	if err != nil {
		return TickResult{Requests: cycle.Requests}, fmt.Errorf("record the cycle of %s: %w", repo, err)
	}
	observed, err := firstObserved(cycle, state.CycleStartedAt, ids)
	if err != nil {
		return TickResult{Requests: cycle.Requests, Recorded: 1, Changed: cycle.Changed}, err
	}
	return TickResult{Requests: cycle.Requests, Recorded: 1, Changed: cycle.Changed, FirstObserved: observed}, nil
}

// firstObserved describes the comments the store reported as new, with the start of the cycle that recorded them
// and of the repository's previous successful one.
func firstObserved(cycle Cycle, previous time.Time, ids []int64) ([]FirstObserved, error) {
	created := map[int64]time.Time{}
	for _, listing := range cycle.Comments {
		for _, comment := range listing {
			created[comment.CommentID] = comment.CreatedAt
		}
	}
	observed := make([]FirstObserved, 0, len(ids))
	for _, id := range ids {
		at, listed := created[id]
		if !listed { // the store is a port boundary: it may only name comments of this cycle
			return nil, fmt.Errorf("the store reported comment %d of %s as new, but the cycle did not list it", id, cycle.Repo)
		}
		observed = append(observed, FirstObserved{Repo: cycle.Repo, CommentID: id, CreatedAt: at, StartedAt: cycle.StartedAt, PreviousStartedAt: previous})
	}
	return observed, nil
}

// list reads every listing of one cycle. The cycle starts at the Date of the first one, GitHub's own clock; a read
// without a Date, or with one more than maxDateAhead ahead of the local clock, fails as decode, since no local clock
// may stand in for it. Even when it fails the returned cycle carries the requests made, for pacing.
func (r *Reconciler) list(ctx context.Context, repo string) (Cycle, error) {
	pulls, info, err := r.reader.OpenPulls(ctx, repo)
	cycle := Cycle{Repo: repo, InstallationID: r.installationID, StartedAt: info.Date, Requests: info.Requests, Changed: info.Changed,
		OpenPulls: pulls, Comments: make(map[int][]ObservedComment, len(pulls))}
	if err != nil {
		return cycle, err
	}
	if info.Date.IsZero() || info.Date.Sub(r.now()) > maxDateAhead {
		return cycle, &ReadError{Kind: FailureDecode}
	}
	for _, pull := range pulls {
		var read ReadInfo
		cycle.Comments[pull.Number], read, err = r.reader.PullComments(ctx, repo, pull.Number)
		cycle.Requests += read.Requests
		cycle.Changed = cycle.Changed || read.Changed
		if err != nil {
			return cycle, err
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
func (r *Reconciler) Tick(ctx context.Context, repos []string, deadline time.Duration) (TickResult, error) {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		results = make([]TickResult, len(repos)) // one slot per repository, so the goroutines never share one
		slots   = make(chan struct{}, maxInFlight)
	)
	for i, repo := range repos {
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
			var err error
			if results[i], err = r.SyncRepo(ctx, repo); err != nil && ctx.Err() == nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var total TickResult
	for _, result := range results { // the requests of a cycle the deadline cut still count
		total.Requests += result.Requests
		total.Recorded += result.Recorded
		total.Changed = total.Changed || result.Changed
		total.FirstObserved = append(total.FirstObserved, result.FirstObserved...)
	}
	return total, errors.Join(errs...)
}
