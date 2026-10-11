package githubsync_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
)

const (
	repo           = "acme/widgets"
	installationID = 77
)

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// clock is a controllable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeReader serves listings and records every call.
type fakeReader struct {
	mu       sync.Mutex
	pulls    map[string][]githubsync.ObservedPull
	comments map[int][]githubsync.ObservedComment
	pullsErr error
	// commentsErr fails the comment listing of one pull number.
	commentsErr map[int]error
	calls       []string
}

func (f *fakeReader) OpenPulls(_ context.Context, repo string) ([]githubsync.ObservedPull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "pulls "+repo)
	return f.pulls[repo], f.pullsErr
}

func (f *fakeReader) PullComments(_ context.Context, repo string, number int) ([]githubsync.ObservedComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "comments "+repo+" "+strconv.Itoa(number))
	return f.comments[number], f.commentsErr[number]
}

func (f *fakeReader) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func pull(number int) githubsync.ObservedPull {
	return githubsync.ObservedPull{Number: number, HeadSHA: "h", HeadRef: "feature", BaseRef: "main", BaseSHA: "b", UpdatedAt: t0.Add(-time.Hour)}
}

func comment(id int64, body string) githubsync.ObservedComment {
	return githubsync.ObservedComment{CommentID: id, AuthorID: 9, AuthorLogin: "octo", CreatedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Minute), Body: body}
}

func newReconciler(t *testing.T, reader githubsync.Reader, store githubsync.Store, c *clock) *githubsync.Reconciler {
	t.Helper()
	r, err := githubsync.NewReconciler(reader, store, c.Now, installationID)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return r
}

func TestACycleRecordsExactlyTheListings(t *testing.T) {
	store := githubsynctest.NewMemory()
	c := &clock{now: t0}
	reader := &fakeReader{
		pulls:    map[string][]githubsync.ObservedPull{repo: {pull(1), pull(2)}},
		comments: map[int][]githubsync.ObservedComment{1: {comment(10, "lgtm"), comment(11, "ship it")}, 2: nil},
	}

	if err := newReconciler(t, reader, store, c).SyncRepo(t.Context(), repo); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	state, found, err := store.Repository(t.Context(), repo)
	if err != nil || !found {
		t.Fatalf("Repository = %v, %v, %v; want the stored state", state, found, err)
	}
	if state.InstallationID != installationID || !state.LastSuccess.Equal(t0) || state.LastError != "" || !state.WaitUntil.IsZero() {
		t.Fatalf("state = %+v; want a success at %v for installation %d", state, t0, installationID)
	}
	pulls, _ := store.Pulls(t.Context(), repo)
	if len(pulls) != 2 || pulls[0].Number != 1 || pulls[1].Number != 2 || !pulls[0].NotOpenSince.IsZero() {
		t.Fatalf("pulls = %+v; want exactly the open listing", pulls)
	}
	versions, _ := store.Comments(t.Context(), repo, 1)
	if len(versions) != 2 || versions[0].Body != "lgtm" || versions[1].Body != "ship it" || !versions[0].FirstSeen.Equal(t0) {
		t.Fatalf("comments of pull 1 = %+v; want exactly the two listed", versions)
	}
	if versions, _ := store.Comments(t.Context(), repo, 2); len(versions) != 0 {
		t.Fatalf("comments of pull 2 = %+v; want none", versions)
	}
}

func TestAPullNoLongerOpenIsNotListedForComments(t *testing.T) {
	store := githubsynctest.NewMemory()
	c := &clock{now: t0}
	reader := &fakeReader{
		pulls:    map[string][]githubsync.ObservedPull{repo: {pull(1), pull(2)}},
		comments: map[int][]githubsync.ObservedComment{1: {comment(10, "a")}, 2: {comment(20, "b")}},
	}
	r := newReconciler(t, reader, store, c)
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	reader.pulls[repo] = []githubsync.ObservedPull{pull(1)}
	reader.calls = nil
	c.Advance(time.Minute)
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	for _, call := range reader.callLog() {
		if call == "comments "+repo+" 2" {
			t.Fatalf("calls = %v; the comments of a pull that is no longer open were listed", reader.callLog())
		}
	}
	pulls, _ := store.Pulls(t.Context(), repo)
	if !pulls[0].NotOpenSince.IsZero() || !pulls[1].NotOpenSince.Equal(t0.Add(time.Minute)) {
		t.Fatalf("pulls = %+v; want pull 2 marked not open at the second cycle", pulls)
	}
	if versions, _ := store.Comments(t.Context(), repo, 2); len(versions) != 1 || !versions[0].NotSeenSince.IsZero() {
		t.Fatalf("comments of pull 2 = %+v; want them untouched", versions)
	}
}

func TestAReadErrorRecordsAFailureAndNoObservation(t *testing.T) {
	reset := t0.Add(20 * time.Minute)
	for name, test := range map[string]struct {
		pullsErr, commentsErr error
		want                  githubsync.Failure
	}{
		"listing pulls: unauthorized": {
			pullsErr: &githubsync.ReadError{Kind: githubsync.FailureUnauthorized, Status: 401},
			want:     githubsync.Failure{Kind: githubsync.FailureUnauthorized, Status: 401},
		},
		"listing comments: transport": {
			commentsErr: &githubsync.ReadError{Kind: githubsync.FailureTransport},
			want:        githubsync.Failure{Kind: githubsync.FailureTransport},
		},
		"listing comments: other status": {
			commentsErr: &githubsync.ReadError{Kind: githubsync.FailureStatus, Status: 502},
			want:        githubsync.Failure{Kind: githubsync.FailureStatus, Status: 502},
		},
		"a wrapped classified error": {
			pullsErr: errors.Join(errors.New("context"), &githubsync.ReadError{Kind: githubsync.FailureDecode}),
			want:     githubsync.Failure{Kind: githubsync.FailureDecode},
		},
		"rate limited": {
			pullsErr: &githubsync.ReadError{Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: reset},
			want:     githubsync.Failure{Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: reset},
		},
		"budget exhausted": {
			commentsErr: &githubsync.ReadError{Kind: githubsync.FailureBudgetExhausted, WaitUntil: reset},
			want:        githubsync.Failure{Kind: githubsync.FailureBudgetExhausted, WaitUntil: reset},
		},
		"rate limited without a reset waits one tick": {
			pullsErr: &githubsync.ReadError{Kind: githubsync.FailureRateLimited, Status: 429},
			want:     githubsync.Failure{Kind: githubsync.FailureRateLimited, Status: 429, WaitUntil: t0.Add(time.Minute)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := githubsynctest.NewMemory()
			c := &clock{now: t0}
			reader := &fakeReader{
				pulls:       map[string][]githubsync.ObservedPull{repo: {pull(1), pull(2)}},
				comments:    map[int][]githubsync.ObservedComment{1: {comment(10, "a")}, 2: {comment(20, "b")}},
				pullsErr:    test.pullsErr,
				commentsErr: map[int]error{2: test.commentsErr}, // the second pull's listing fails after the first succeeded
			}

			if err := newReconciler(t, reader, store, c).SyncRepo(t.Context(), repo); err != nil {
				t.Fatalf("SyncRepo = %v; a failed cycle is recorded, not returned", err)
			}

			state, found, _ := store.Repository(t.Context(), repo)
			if !found || state.LastError != test.want.Kind || state.LastStatus != test.want.Status || !state.WaitUntil.Equal(test.want.WaitUntil) || !state.LastSuccess.IsZero() {
				t.Fatalf("state = %+v; want failure %+v and no success", state, test.want)
			}
			if pulls, _ := store.Pulls(t.Context(), repo); len(pulls) != 0 {
				t.Fatalf("pulls = %+v; a failed cycle must record no observation", pulls)
			}
			if versions, _ := store.Comments(t.Context(), repo, 1); len(versions) != 0 {
				t.Fatalf("comments = %+v; a failed cycle must record no observation", versions)
			}
		})
	}
}

func TestAFailureAfterASuccessKeepsTheObservations(t *testing.T) {
	store := githubsynctest.NewMemory()
	c := &clock{now: t0}
	reader := &fakeReader{pulls: map[string][]githubsync.ObservedPull{repo: {pull(1)}}, comments: map[int][]githubsync.ObservedComment{1: {comment(10, "a")}}}
	r := newReconciler(t, reader, store, c)
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	reader.pullsErr = &githubsync.ReadError{Kind: githubsync.FailureTransport}
	c.Advance(time.Minute)
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	state, _, _ := store.Repository(t.Context(), repo)
	if state.LastError != githubsync.FailureTransport || !state.LastSuccess.Equal(t0) {
		t.Fatalf("state = %+v; want the failure beside the earlier success", state)
	}
	pulls, _ := store.Pulls(t.Context(), repo)
	if len(pulls) != 1 || !pulls[0].NotOpenSince.IsZero() {
		t.Fatalf("pulls = %+v; a failure must not mark anything not open", pulls)
	}
}

func TestARepositoryStillWaitingIsNotCalled(t *testing.T) {
	store := githubsynctest.NewMemory()
	c := &clock{now: t0}
	until := t0.Add(10 * time.Minute)
	if err := store.RecordFailure(t.Context(), githubsync.Failure{Repo: repo, InstallationID: installationID, At: t0, Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: until}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeReader{pulls: map[string][]githubsync.ObservedPull{repo: {pull(1)}}, comments: map[int][]githubsync.ObservedComment{1: nil}}
	r := newReconciler(t, reader, store, c)

	c.Advance(5 * time.Minute)
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if calls := reader.callLog(); len(calls) != 0 {
		t.Fatalf("calls = %v; a waiting repository must not be called", calls)
	}
	state, _, _ := store.Repository(t.Context(), repo)
	if state.LastError != githubsync.FailureRateLimited || !state.WaitUntil.Equal(until) {
		t.Fatalf("state = %+v; want it unchanged while waiting", state)
	}

	c.Advance(5 * time.Minute) // exactly wait_until: the wait has passed
	if err := r.SyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	state, _, _ = store.Repository(t.Context(), repo)
	if len(reader.callLog()) == 0 || state.LastError != "" || !state.WaitUntil.IsZero() || !state.LastSuccess.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("calls %v, state %+v; want a cycle once wait_until passed", reader.callLog(), state)
	}
}

func TestAnUnclassifiedErrorOrACanceledContextRecordsNothing(t *testing.T) {
	for name, test := range map[string]struct {
		readerErr error
		cancel    bool
	}{
		"unclassified": {readerErr: errors.New("boom")},
		"canceled":     {readerErr: context.Canceled, cancel: true},
	} {
		t.Run(name, func(t *testing.T) {
			store := githubsynctest.NewMemory()
			reader := &fakeReader{pullsErr: test.readerErr}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancel {
				cancel()
			}

			if err := newReconciler(t, reader, store, &clock{now: t0}).SyncRepo(ctx, repo); err == nil {
				t.Fatal("SyncRepo = nil; want the error, since it is no classified GitHub failure")
			}
			if _, found, _ := store.Repository(t.Context(), repo); found {
				t.Fatal("a cycle that was not finished or classified recorded a state")
			}
		})
	}
}

func TestNewReconcilerRejectsMissingDependencies(t *testing.T) {
	reader, store, now := &fakeReader{}, githubsynctest.NewMemory(), func() time.Time { return t0 }
	for name, args := range map[string]struct {
		reader githubsync.Reader
		store  githubsync.Store
		now    func() time.Time
		id     int64
	}{
		"reader":       {nil, store, now, 1},
		"store":        {reader, nil, now, 1},
		"clock":        {reader, store, nil, 1},
		"installation": {reader, store, now, 0},
	} {
		if _, err := githubsync.NewReconciler(args.reader, args.store, args.now, args.id); err == nil {
			t.Errorf("NewReconciler without %s = nil; want an error", name)
		}
	}
}

// gatedReader blocks every OpenPulls on gate (or its context) and tracks how many run at once.
type gatedReader struct {
	fakeReader
	gate     chan struct{}
	started  chan string
	inflight int
	max      int
}

func (g *gatedReader) OpenPulls(ctx context.Context, repo string) ([]githubsync.ObservedPull, error) {
	g.mu.Lock()
	g.inflight++
	g.max = max(g.max, g.inflight)
	g.mu.Unlock()
	g.started <- repo
	defer func() {
		g.mu.Lock()
		g.inflight--
		g.mu.Unlock()
	}()
	select {
	case <-g.gate:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func repos(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = "acme/r" + strconv.Itoa(i)
	}
	return names
}

func TestATickRunsAtMostFourCyclesAtOnce(t *testing.T) {
	store := githubsynctest.NewMemory()
	reader := &gatedReader{gate: make(chan struct{}), started: make(chan string, 20)}
	r := newReconciler(t, reader, store, &clock{now: t0})
	all := repos(20)
	done := make(chan error, 1)
	go func() { done <- r.Tick(t.Context(), all, time.Minute) }()

	// Four start; then each release finishes one cycle and lets the next start.
	for range 4 {
		<-reader.started
	}
	for range 20 {
		reader.gate <- struct{}{}
	}
	if err := <-done; err != nil {
		t.Fatalf("Tick = %v", err)
	}

	if reader.max != 4 {
		t.Fatalf("%d cycles ran at once at most; want exactly 4", reader.max)
	}
	for _, name := range all {
		if _, found, _ := store.Repository(t.Context(), name); !found {
			t.Errorf("repository %s was not reconciled", name)
		}
	}
}

func TestATickDeadlineStopsNewRepositoriesAndRecordsOnlyFinishedCycles(t *testing.T) {
	store := githubsynctest.NewMemory()
	reader := &gatedReader{gate: make(chan struct{}), started: make(chan string, 20)} // the gate never opens: no cycle finishes
	r := newReconciler(t, reader, store, &clock{now: t0})
	all := repos(8)

	if err := r.Tick(t.Context(), all, 100*time.Millisecond); err != nil {
		t.Fatalf("Tick = %v; the deadline is an expected end", err)
	}

	if started := len(reader.started); started != 4 {
		t.Fatalf("%d repositories were started; want only the 4 in flight when the deadline passed", started)
	}
	for _, name := range all {
		if _, found, _ := store.Repository(t.Context(), name); found {
			t.Errorf("repository %s recorded a state although its cycle did not finish", name)
		}
	}
}

// selectiveReader finishes the repositories in finish at once and blocks every other until its context ends.
type selectiveReader struct {
	finish  map[string]bool
	blocked chan string
}

func (s *selectiveReader) OpenPulls(ctx context.Context, repo string) ([]githubsync.ObservedPull, error) {
	if s.finish[repo] {
		return nil, nil
	}
	s.blocked <- repo
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *selectiveReader) PullComments(context.Context, string, int) ([]githubsync.ObservedComment, error) {
	return nil, nil
}

func TestACanceledTickRecordsOnlyFinishedCycles(t *testing.T) {
	store := githubsynctest.NewMemory()
	reader := &selectiveReader{finish: map[string]bool{"acme/r0": true, "acme/r1": true}, blocked: make(chan string, 20)}
	r := newReconciler(t, reader, store, &clock{now: t0})
	all := repos(6)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Tick(ctx, all, time.Minute) }()

	for range 4 { // the four unfinished repositories are all in flight, so the finished two are done
		<-reader.blocked
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Tick = %v; a canceled context is an expected end", err)
	}

	for _, name := range all {
		if _, found, _ := store.Repository(t.Context(), name); found != reader.finish[name] {
			t.Errorf("repository %s recorded = %v; want %v", name, found, reader.finish[name])
		}
	}
}

type failingStore struct {
	githubsync.Store
	err error
}

func (f *failingStore) RecordCycle(context.Context, githubsync.Cycle) error { return f.err }

func TestATickReturnsAStoreFailure(t *testing.T) {
	failing := &failingStore{Store: githubsynctest.NewMemory(), err: errors.New("database down")}
	err := newReconciler(t, &fakeReader{}, failing, &clock{now: t0}).Tick(t.Context(), repos(3), time.Minute)
	if !errors.Is(err, failing.err) {
		t.Fatalf("Tick = %v; want the store failure", err)
	}
}
