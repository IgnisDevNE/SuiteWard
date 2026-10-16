package githubsync_test

import (
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
)

func stored(t *testing.T, store githubsync.Store, name string) githubsync.RepositoryState {
	t.Helper()
	state, found, err := store.Repository(t.Context(), name)
	if err != nil || !found {
		t.Fatalf("Repository(%s) = %+v, %v, %v; want the stored state", name, state, found, err)
	}
	return state
}

func sameObserved(a, b githubsync.FirstObserved) bool {
	return a.Repo == b.Repo && a.CommentID == b.CommentID && a.CreatedAt.Equal(b.CreatedAt) && a.StartedAt.Equal(b.StartedAt) && a.PreviousStartedAt.Equal(b.PreviousStartedAt)
}

// The cycle starts at the Date of its open pulls listing, GitHub's clock, and never at a comments listing's.
func TestACycleStartsAtTheDateOfTheOpenPullsListing(t *testing.T) {
	store := githubsynctest.NewMemory()
	start, other := t0.Add(-3*time.Second), t0.Add(-1*time.Second)
	reader := &fakeReader{
		pulls:       map[string][]githubsync.ObservedPull{repo: {pull(1), pull(2)}},
		comments:    map[int][]githubsync.ObservedComment{1: {comment(10, "lgtm")}, 2: nil},
		openInfo:    &githubsync.ReadInfo{Date: start, Requests: 1},
		commentInfo: &githubsync.ReadInfo{Date: other, Requests: 1},
	}

	if _, err := newReconciler(t, reader, store, &clock{now: t0}).SyncRepo(t.Context(), repo); err != nil {
		t.Fatalf("SyncRepo: %v", err)
	}

	if got := stored(t, store, repo).CycleStartedAt; !got.Equal(start) {
		t.Fatalf("CycleStartedAt = %v, want the open pulls listing's Date %v (the comments listings said %v)", got, start, other)
	}
}

// Without a Date there is no GitHub clock for the cycle: it fails as decode and never falls back to the local clock.
func TestACycleWithoutAnOpenPullsDateFailsAsDecode(t *testing.T) {
	store := githubsynctest.NewMemory()
	reader := &fakeReader{
		pulls:    map[string][]githubsync.ObservedPull{repo: {pull(1)}},
		comments: map[int][]githubsync.ObservedComment{1: {comment(10, "lgtm")}},
		openInfo: &githubsync.ReadInfo{Requests: 1}, // answered with no usable Date
	}

	result, err := newReconciler(t, reader, store, &clock{now: t0}).SyncRepo(t.Context(), repo)

	if err != nil {
		t.Fatalf("SyncRepo = %v; the failed cycle is recorded, not returned", err)
	}
	state := stored(t, store, repo)
	if state.LastError != githubsync.FailureDecode || state.LastStatus != 0 || !state.LastSuccess.IsZero() || !state.CycleStartedAt.IsZero() {
		t.Errorf("state = %+v; want the decode failure and no success or start", state)
	}
	if pulls, _ := store.Pulls(t.Context(), repo); len(pulls) != 0 {
		t.Errorf("pulls = %+v; want nothing recorded", pulls)
	}
	if calls := reader.callLog(); len(calls) != 1 {
		t.Errorf("calls = %v; want only the open pulls listing", calls)
	}
	if len(result.FirstObserved) != 0 {
		t.Errorf("result = %+v; want no observation", result)
	}
}

func TestACycleSumsTheRequestsAndChangesOfItsListings(t *testing.T) {
	for name, test := range map[string]struct {
		open, comments githubsync.ReadInfo
		wantRequests   int
		wantChanged    bool
	}{
		"nothing changed":    {githubsync.ReadInfo{Date: t0, Requests: 2}, githubsync.ReadInfo{Requests: 1}, 2 + 3*1, false},
		"only the pulls":     {githubsync.ReadInfo{Date: t0, Requests: 1, Changed: true}, githubsync.ReadInfo{Requests: 2}, 1 + 3*2, true},
		"only the comments":  {githubsync.ReadInfo{Date: t0, Requests: 1}, githubsync.ReadInfo{Requests: 1, Changed: true}, 1 + 3*1, true},
		"everything changed": {githubsync.ReadInfo{Date: t0, Requests: 4, Changed: true}, githubsync.ReadInfo{Requests: 1, Changed: true}, 4 + 3*1, true},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{
				pulls:       map[string][]githubsync.ObservedPull{repo: {pull(1), pull(2), pull(3)}},
				comments:    map[int][]githubsync.ObservedComment{1: nil, 2: nil, 3: nil},
				openInfo:    &test.open,
				commentInfo: &test.comments,
			}

			result, err := newReconciler(t, reader, githubsynctest.NewMemory(), &clock{now: t0}).SyncRepo(t.Context(), repo)

			if err != nil || result.Requests != test.wantRequests || result.Changed != test.wantChanged {
				t.Errorf("SyncRepo = %+v, %v; want %d requests, changed %t", result, err, test.wantRequests, test.wantChanged)
			}
		})
	}
}

// Only a comment the store had never seen is first observed: an edit and a repeat are not.
func TestFirstObservedHoldsOnlyTheNewCommentsWithTheirStartAndThePreviousOne(t *testing.T) {
	store := githubsynctest.NewMemory()
	c := &clock{now: t0}
	firstStart, secondStart := t0.Add(-time.Second), t0.Add(59*time.Second)
	reader := &fakeReader{
		pulls:    map[string][]githubsync.ObservedPull{repo: {pull(1)}},
		comments: map[int][]githubsync.ObservedComment{1: {comment(10, "lgtm"), comment(11, "note")}},
		openInfo: &githubsync.ReadInfo{Date: firstStart, Requests: 1},
	}
	r := newReconciler(t, reader, store, c)

	first, err := r.SyncRepo(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []githubsync.FirstObserved{
		{Repo: repo, CommentID: 10, CreatedAt: comment(10, "").CreatedAt, StartedAt: firstStart},
		{Repo: repo, CommentID: 11, CreatedAt: comment(11, "").CreatedAt, StartedAt: firstStart},
	}
	if !slices.EqualFunc(first.FirstObserved, want, sameObserved) {
		t.Fatalf("first cycle FirstObserved = %+v, want %+v (no previous start)", first.FirstObserved, want)
	}

	c.Advance(time.Minute)
	reader.openInfo = &githubsync.ReadInfo{Date: secondStart, Requests: 1}
	reader.comments[1] = []githubsync.ObservedComment{comment(10, "lgtm"), comment(11, "note, edited"), comment(12, "new")}
	second, err := r.SyncRepo(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want = []githubsync.FirstObserved{{Repo: repo, CommentID: 12, CreatedAt: comment(12, "").CreatedAt, StartedAt: secondStart, PreviousStartedAt: firstStart}}
	if !slices.EqualFunc(second.FirstObserved, want, sameObserved) {
		t.Fatalf("second cycle FirstObserved = %+v, want only the new comment %+v", second.FirstObserved, want)
	}
}

func TestATickSumsTheResultsOfItsRepositories(t *testing.T) {
	all := repos(3)
	reader := &fakeReader{
		pulls:       map[string][]githubsync.ObservedPull{},
		comments:    map[int][]githubsync.ObservedComment{1: {comment(10, "lgtm")}},
		openInfo:    &githubsync.ReadInfo{Date: t0, Requests: 2},
		commentInfo: &githubsync.ReadInfo{Requests: 3, Changed: true},
	}
	for _, name := range all {
		reader.pulls[name] = []githubsync.ObservedPull{pull(1)}
	}

	result, err := newReconciler(t, reader, githubsynctest.NewMemory(), &clock{now: t0}).Tick(t.Context(), all, time.Minute)

	if err != nil || result.Requests != 3*(2+3) || !result.Changed {
		t.Fatalf("Tick = %+v, %v; want %d requests, changed", result, err, 3*(2+3))
	}
	var seen []string
	for _, observed := range result.FirstObserved {
		if observed.CommentID != 10 {
			t.Errorf("FirstObserved %+v; want comment 10", observed)
		}
		seen = append(seen, observed.Repo)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, all) {
		t.Errorf("FirstObserved repositories = %v, want %v", seen, all)
	}
}

// A Tick whose cycles all came back as 304s reports its requests but no change and nothing first observed.
func TestATickOfRepositoriesThatChangedNothingReportsNoChange(t *testing.T) {
	reader := &fakeReader{
		pulls:    map[string][]githubsync.ObservedPull{repo: {pull(1)}},
		comments: map[int][]githubsync.ObservedComment{1: nil},
		openInfo: &githubsync.ReadInfo{Date: t0, Requests: 1},
	}

	result, err := newReconciler(t, reader, githubsynctest.NewMemory(), &clock{now: t0}).Tick(t.Context(), []string{repo}, time.Minute)

	if err != nil || result.Changed || result.Requests != 2 || len(result.FirstObserved) != 0 {
		t.Fatalf("Tick = %+v, %v; want 2 requests, no change, nothing first observed", result, err)
	}
}
