// Package githubsynctest provides an in-memory githubsync.Store and the
// conformance suite that every githubsync.Store must pass.
package githubsynctest

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

const (
	gsRepo         = "acme/widgets"
	gsOtherRepo    = "acme/gadgets"
	gsInstallation = int64(42)
)

// gsTime is a time at microsecond precision, which is all PostgreSQL stores,
// n minutes after a fixed origin.
func gsTime(n int) time.Time {
	return time.Date(2026, 10, 2, 12, 0, 0, 123456000, time.UTC).Add(time.Duration(n) * time.Minute)
}

func gsPull(number int, head string) githubsync.ObservedPull {
	return githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: fmt.Sprintf("feature-%d", number), BaseRef: "main", BaseSHA: "base-sha", UpdatedAt: gsTime(number)}
}

// gsComment is comment id at its edit-th version; an id keeps its CreatedAt.
func gsComment(id int64, body string, edit int) githubsync.ObservedComment {
	return githubsync.ObservedComment{CommentID: id, AuthorID: 7, AuthorLogin: "reviewer", CreatedAt: gsTime(int(id)), UpdatedAt: gsTime(100 + edit), Body: body}
}

type gsListing = map[int][]githubsync.ObservedComment

// gsCycleOf builds a valid cycle of repo at time at: every pull gets a listing, empty unless comments has one.
func gsCycleOf(repo string, at int, pulls []githubsync.ObservedPull, comments gsListing) githubsync.Cycle {
	full := gsListing{}
	for _, pull := range pulls {
		full[pull.Number] = comments[pull.Number]
	}
	return githubsync.Cycle{Repo: repo, InstallationID: gsInstallation, At: gsTime(at), OpenPulls: pulls, Comments: full}
}

func gsCycle(at int, pulls []githubsync.ObservedPull, comments gsListing) githubsync.Cycle {
	return gsCycleOf(gsRepo, at, pulls, comments)
}

func gsPulls(pulls ...githubsync.ObservedPull) []githubsync.ObservedPull { return pulls }

func gsComments(comments ...githubsync.ObservedComment) []githubsync.ObservedComment {
	return comments
}

// gsView is everything the port can read about one repository, with empty
// listings normalized to nil.
type gsView struct {
	Found      bool
	Repository githubsync.RepositoryState
	Pulls      []githubsync.PullState
	Comments   map[int][]githubsync.CommentVersion
}

func gsNothing() gsView { return gsView{Comments: map[int][]githubsync.CommentVersion{}} }

type gsEnv struct {
	t     *testing.T
	store githubsync.Store
}

func (e *gsEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *gsEnv) record(cycle githubsync.Cycle) {
	e.t.Helper()
	e.must(e.store.RecordCycle(context.Background(), cycle))
}

func (e *gsEnv) fail(failure githubsync.Failure) {
	e.t.Helper()
	e.must(e.store.RecordFailure(context.Background(), failure))
}

func (e *gsEnv) viewOf(repo string) gsView {
	e.t.Helper()
	ctx := context.Background()
	var view gsView
	var err error
	if view.Repository, view.Found, err = e.store.Repository(ctx, repo); err != nil {
		e.t.Fatal(err)
	}
	if view.Pulls, err = e.store.Pulls(ctx, repo); err != nil {
		e.t.Fatal(err)
	}
	if len(view.Pulls) == 0 {
		view.Pulls = nil
	}
	view.Comments = map[int][]githubsync.CommentVersion{}
	for number := 1; number <= 3; number++ {
		versions, err := e.store.Comments(ctx, repo, number)
		if err != nil {
			e.t.Fatal(err)
		}
		if len(versions) > 0 {
			view.Comments[number] = versions
		}
	}
	return view
}

func (e *gsEnv) view() gsView { e.t.Helper(); return e.viewOf(gsRepo) }

func (e *gsEnv) requireView(got, want gsView) {
	e.t.Helper()
	if !reflect.DeepEqual(got, want) {
		e.t.Fatalf("state\n got %+v\nwant %+v", got, want)
	}
}

func (e *gsEnv) pull(number int) githubsync.PullState {
	e.t.Helper()
	for _, pull := range e.view().Pulls {
		if pull.Number == number {
			return pull
		}
	}
	e.t.Fatalf("pull %d not stored", number)
	return githubsync.PullState{}
}

func gsVersion(pull int, comment githubsync.ObservedComment, firstSeen int, notSeenSince time.Time) githubsync.CommentVersion {
	return githubsync.CommentVersion{PullNumber: pull, CommentID: comment.CommentID, AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin,
		CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, Body: comment.Body, BodyDigest: artifact.Hash([]byte(comment.Body)),
		FirstSeen: gsTime(firstSeen), NotSeenSince: notSeenSince}
}

// RunConformance runs the observation semantics of the persistence contract
// against stores created by newStore. Every subtest gets its own empty store.
func RunConformance(t *testing.T, newStore func(t *testing.T) githubsync.Store) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*gsEnv, func(*testing.T) githubsync.Store)
	}{
		{"RepositoryIsUnknownBeforeItsFirstCycle", gsUnknownRepository},
		{"FirstCycleCreatesTheState", gsFirstCycle},
		{"RepeatedIdenticalCycleChangesNothing", gsRepeatedCycle},
		{"LaterCycleOnlyMovesObservationTimes", gsLaterCycle},
		{"HeadChangeUpdatesThePullAndKeepsFirstSeen", gsHeadChange},
		{"PullMissingFromTheOpenListingIsMarkedAndReappearingClearsIt", gsPullNotOpen},
		{"EditAddsAVersionAndKeepsTheEarlierBody", gsEdit},
		{"DifferentCreatedAtForAnIDIsAnErrorAndRecordsNothing", gsCreatedAtChange},
		{"CommentMissingFromItsOpenPullIsMarkedAndReappearingClearsIt", gsCommentAbsent},
		{"ReappearingCommentWithAnEditClearsTheMarkOfEveryVersion", gsCommentReappearsEdited},
		{"CommentsOfAPullNoLongerOpenAreUntouched", gsClosedPullComments},
		{"FailureRecordsKindStatusAndWaitWithoutTouchingObservations", gsFailure},
		{"FailureBeforeTheFirstCycleCreatesTheRepository", gsFailureFirst},
		{"LaterSuccessClearsTheErrorAndTheWait", gsRecovery},
		{"InvalidInputIsRejectedWithoutEffects", gsInvalidInput},
		{"RepositoriesAreIndependent", gsIndependentRepositories},
		{"CanceledContextRecordsNothing", gsCanceled},
		{"ConcurrentCyclesOfOneRepositoryEndAsOneAppliedLast", gsConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v\n%s", r, debug.Stack())
				}
			}()
			tc.run(&gsEnv{t: t, store: newStore(t)}, newStore)
		})
	}
}

func gsUnknownRepository(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.requireView(e.view(), gsNothing())
}

func gsFirstCycle(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	approve, note := gsComment(10, "approved", 0), gsComment(11, "note", 0)
	e.record(gsCycle(1, gsPulls(gsPull(1, "head-1"), gsPull(2, "head-2")), gsListing{1: gsComments(approve, note)}))

	want := gsView{
		Found:      true,
		Repository: githubsync.RepositoryState{Repo: gsRepo, InstallationID: gsInstallation, LastSuccess: gsTime(1)},
		Pulls: []githubsync.PullState{
			{Number: 1, HeadSHA: "head-1", HeadRef: "feature-1", BaseRef: "main", BaseSHA: "base-sha", UpdatedAt: gsTime(1), FirstSeen: gsTime(1), LastSeen: gsTime(1)},
			{Number: 2, HeadSHA: "head-2", HeadRef: "feature-2", BaseRef: "main", BaseSHA: "base-sha", UpdatedAt: gsTime(2), FirstSeen: gsTime(1), LastSeen: gsTime(1)},
		},
		Comments: map[int][]githubsync.CommentVersion{1: {gsVersion(1, approve, 1, time.Time{}), gsVersion(1, note, 1, time.Time{})}},
	}
	e.requireView(e.view(), want)
}

func gsRepeatedCycle(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	cycle := gsCycle(1, gsPulls(gsPull(1, "head-1")), gsListing{1: gsComments(gsComment(10, "approved", 0))})
	e.record(cycle)
	first := e.view()
	e.record(cycle)
	e.requireView(e.view(), first)
}

func gsLaterCycle(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	comment := gsComment(10, "approved", 0)
	e.record(gsCycle(1, gsPulls(gsPull(1, "head-1")), gsListing{1: gsComments(comment)}))
	e.record(gsCycle(2, gsPulls(gsPull(1, "head-1")), gsListing{1: gsComments(comment)}))

	got := e.view()
	if got.Repository.LastSuccess != gsTime(2) {
		e.t.Fatalf("last success %v, want %v", got.Repository.LastSuccess, gsTime(2))
	}
	if pull := got.Pulls[0]; pull.FirstSeen != gsTime(1) || pull.LastSeen != gsTime(2) {
		e.t.Fatalf("pull seen %v to %v, want %v to %v", pull.FirstSeen, pull.LastSeen, gsTime(1), gsTime(2))
	}
	if want := []githubsync.CommentVersion{gsVersion(1, comment, 1, time.Time{})}; !reflect.DeepEqual(got.Comments[1], want) {
		e.t.Fatalf("comments %+v, want the single first version %+v", got.Comments[1], want)
	}
}

func gsHeadChange(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "head-1")), nil))
	moved := gsPull(1, "head-2")
	moved.HeadRef, moved.BaseRef, moved.BaseSHA, moved.UpdatedAt = "renamed", "develop", "base-2", gsTime(9)
	e.record(gsCycle(2, gsPulls(moved), nil))

	want := githubsync.PullState{Number: 1, HeadSHA: "head-2", HeadRef: "renamed", BaseRef: "develop", BaseSHA: "base-2", UpdatedAt: gsTime(9), FirstSeen: gsTime(1), LastSeen: gsTime(2)}
	if got := e.pull(1); got != want {
		e.t.Fatalf("pull %+v, want %+v", got, want)
	}
}

func gsPullNotOpen(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil))
	if got := e.pull(2); got.NotOpenSince != gsTime(2) || got.LastSeen != gsTime(1) {
		e.t.Fatalf("pull 2 not open since %v last seen %v, want %v and %v", got.NotOpenSince, got.LastSeen, gsTime(2), gsTime(1))
	}
	if got := e.pull(1); !got.NotOpenSince.IsZero() {
		e.t.Fatalf("pull 1 is still open but not open since %v", got.NotOpenSince)
	}

	e.record(gsCycle(3, gsPulls(gsPull(1, "a")), nil))
	if got := e.pull(2); got.NotOpenSince != gsTime(2) {
		e.t.Fatalf("pull 2 not open since %v after a second absence, want the first, %v", got.NotOpenSince, gsTime(2))
	}

	e.record(gsCycle(4, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil))
	if got := e.pull(2); !got.NotOpenSince.IsZero() || got.FirstSeen != gsTime(1) || got.LastSeen != gsTime(4) {
		e.t.Fatalf("reappeared pull %+v, want open, first seen %v, last seen %v", got, gsTime(1), gsTime(4))
	}
}

func gsEdit(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	original, edited := gsComment(10, "approved", 0), gsComment(10, "approved, but only for now", 1)
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(original)}))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(edited)}))

	want := []githubsync.CommentVersion{gsVersion(1, original, 1, time.Time{}), gsVersion(1, edited, 2, time.Time{})}
	if got := e.view().Comments[1]; !reflect.DeepEqual(got, want) {
		e.t.Fatalf("versions\n got %+v\nwant %+v", got, want)
	}
}

func gsCreatedAtChange(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(gsComment(10, "approved", 0))}))
	before := e.view()

	// The pull changes in the same cycle, so a partial write would be visible.
	forged := gsComment(10, "approved", 1)
	forged.CreatedAt = gsTime(50)
	if err := e.store.RecordCycle(context.Background(), gsCycle(2, gsPulls(gsPull(1, "moved")), gsListing{1: gsComments(forged)})); err == nil {
		e.t.Fatal("a different created_at for a known comment id was recorded")
	}
	e.requireView(e.view(), before)
}

func gsCommentAbsent(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	kept, first, own := gsComment(10, "kept", 0), gsComment(11, "first", 0), gsComment(20, "other pull", 0)
	second := gsComment(11, "second", 1)
	both := gsPulls(gsPull(1, "a"), gsPull(2, "b"))
	e.record(gsCycle(1, both, gsListing{1: gsComments(kept, first), 2: gsComments(own)}))
	e.record(gsCycle(2, both, gsListing{1: gsComments(kept, second), 2: gsComments(own)}))

	// Comment 11 disappears: only its latest version is marked, and nothing on pull 2 changes.
	e.record(gsCycle(3, both, gsListing{1: gsComments(kept), 2: gsComments(own)}))
	got := e.view().Comments
	if want := []githubsync.CommentVersion{gsVersion(1, kept, 1, time.Time{}), gsVersion(1, first, 1, time.Time{}), gsVersion(1, second, 2, gsTime(3))}; !reflect.DeepEqual(got[1], want) {
		e.t.Fatalf("pull 1 versions\n got %+v\nwant %+v", got[1], want)
	}
	if want := []githubsync.CommentVersion{gsVersion(2, own, 1, time.Time{})}; !reflect.DeepEqual(got[2], want) {
		e.t.Fatalf("pull 2 versions\n got %+v\nwant %+v", got[2], want)
	}

	// A second absence keeps the first time; reappearing clears the mark.
	e.record(gsCycle(4, both, gsListing{1: gsComments(kept), 2: gsComments(own)}))
	if got := e.view().Comments[1][2].NotSeenSince; got != gsTime(3) {
		e.t.Fatalf("not seen since %v after a second absence, want the first, %v", got, gsTime(3))
	}
	e.record(gsCycle(5, both, gsListing{1: gsComments(kept, second), 2: gsComments(own)}))
	if got := e.view().Comments[1][2]; !got.NotSeenSince.IsZero() {
		e.t.Fatalf("reappeared comment still not seen since %v", got.NotSeenSince)
	}
}

func gsCommentReappearsEdited(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	first, second := gsComment(10, "first", 0), gsComment(10, "second", 1)
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(first)}))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil))
	e.record(gsCycle(3, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(second)}))

	want := []githubsync.CommentVersion{gsVersion(1, first, 1, time.Time{}), gsVersion(1, second, 3, time.Time{})}
	if got := e.view().Comments[1]; !reflect.DeepEqual(got, want) {
		e.t.Fatalf("versions\n got %+v\nwant %+v", got, want)
	}
}

func gsClosedPullComments(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	comment := gsComment(20, "on pull 2", 0)
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b")), gsListing{2: gsComments(comment)}))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil))

	want := []githubsync.CommentVersion{gsVersion(2, comment, 1, time.Time{})}
	if got := e.view().Comments[2]; !reflect.DeepEqual(got, want) {
		e.t.Fatalf("comments of a pull no longer open\n got %+v\nwant %+v", got, want)
	}
}

func gsFailure(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(gsComment(10, "approved", 0))}))
	before := e.view()

	e.fail(githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation + 1, At: gsTime(2), Kind: githubsync.FailureRateLimited, Status: 403, WaitUntil: gsTime(10)})

	after := e.view()
	want := before.Repository
	want.InstallationID, want.LastError, want.LastStatus, want.WaitUntil = gsInstallation+1, githubsync.FailureRateLimited, 403, gsTime(10)
	if after.Repository != want {
		e.t.Fatalf("repository\n got %+v\nwant %+v", after.Repository, want)
	}
	before.Repository = after.Repository
	e.requireView(after, before)
}

func gsFailureFirst(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.fail(githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1), Kind: githubsync.FailureTransport})

	want := githubsync.RepositoryState{Repo: gsRepo, InstallationID: gsInstallation, LastError: githubsync.FailureTransport}
	if got := e.view(); !got.Found || got.Repository != want || len(got.Pulls) != 0 {
		e.t.Fatalf("state %+v, want a stored repository %+v without pulls", got, want)
	}
}

func gsRecovery(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	e.fail(githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(2), Kind: githubsync.FailureBudgetExhausted, Status: 200, WaitUntil: gsTime(30)})
	e.record(gsCycle(3, gsPulls(gsPull(1, "a")), nil))

	want := githubsync.RepositoryState{Repo: gsRepo, InstallationID: gsInstallation, LastSuccess: gsTime(3)}
	if got := e.view().Repository; got != want {
		e.t.Fatalf("repository %+v, want %+v", got, want)
	}
}

func gsInvalidInput(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	ctx := context.Background()
	valid := func(change func(*githubsync.Cycle)) githubsync.Cycle {
		cycle := gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(gsComment(10, "x", 0))})
		change(&cycle)
		return cycle
	}
	cycles := map[string]githubsync.Cycle{
		"no repository":                       valid(func(c *githubsync.Cycle) { c.Repo = " " }),
		"no installation":                     valid(func(c *githubsync.Cycle) { c.InstallationID = 0 }),
		"no time":                             valid(func(c *githubsync.Cycle) { c.At = time.Time{} }),
		"pull listed twice":                   valid(func(c *githubsync.Cycle) { c.OpenPulls = gsPulls(gsPull(1, "a"), gsPull(1, "b")) }),
		"comments of a pull that is not open": valid(func(c *githubsync.Cycle) { c.Comments[2] = gsComments(gsComment(20, "x", 0)) }),
		"no listing for an open pull":         valid(func(c *githubsync.Cycle) { c.Comments = gsListing{} }),
		"comment listed twice": valid(func(c *githubsync.Cycle) {
			c.Comments[1] = gsComments(gsComment(10, "x", 0), gsComment(10, "y", 1))
		}),
		"comment without a time": valid(func(c *githubsync.Cycle) {
			c.Comments[1] = gsComments(githubsync.ObservedComment{CommentID: 10})
		}),
	}
	for name, cycle := range cycles {
		if err := e.store.RecordCycle(ctx, cycle); err == nil {
			e.t.Fatalf("cycle with %s was recorded", name)
		}
	}
	failures := map[string]githubsync.Failure{
		"unknown kind":    {Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1), Kind: "boom"},
		"no kind":         {Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1)},
		"invalid status":  {Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1), Kind: githubsync.FailureStatus, Status: 42},
		"no installation": {Repo: gsRepo, At: gsTime(1), Kind: githubsync.FailureStatus, Status: 500},
	}
	for name, failure := range failures {
		if err := e.store.RecordFailure(ctx, failure); err == nil {
			e.t.Fatalf("failure with %s was recorded", name)
		}
	}
	e.requireView(e.view(), gsNothing())
}

func gsIndependentRepositories(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), gsListing{1: gsComments(gsComment(10, "x", 0))}))
	e.record(gsCycleOf(gsOtherRepo, 1, gsPulls(gsPull(1, "other")), gsListing{1: gsComments(gsComment(10, "y", 0))}))
	before := e.view()

	e.record(gsCycleOf(gsOtherRepo, 2, nil, nil)) // nothing is open in the other repository any more
	e.fail(githubsync.Failure{Repo: gsOtherRepo, InstallationID: gsInstallation, At: gsTime(3), Kind: githubsync.FailureNotFound, Status: 404})

	e.requireView(e.view(), before)
	if got := e.viewOf(gsOtherRepo); len(got.Pulls) != 1 || got.Pulls[0].NotOpenSince != gsTime(2) || got.Repository.LastError != githubsync.FailureNotFound {
		e.t.Fatalf("other repository %+v, want its pull closed and its failure stored", got)
	}
}

func gsCanceled(e *gsEnv, _ func(*testing.T) githubsync.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.store.RecordCycle(ctx, gsCycle(1, gsPulls(gsPull(1, "a")), nil)); err == nil {
		e.t.Fatal("a cycle was recorded on a canceled context")
	}
	if err := e.store.RecordFailure(ctx, githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1), Kind: githubsync.FailureTransport}); err == nil {
		e.t.Fatal("a failure was recorded on a canceled context")
	}
	e.requireView(e.view(), gsNothing())
}

// gsConcurrent has two cycles of one repository race; the result must equal
// the cycles applied one after the other, in either order.
func gsConcurrent(e *gsEnv, newStore func(*testing.T) githubsync.Store) {
	x := gsCycle(1, gsPulls(gsPull(1, "x1"), gsPull(2, "x2")), gsListing{1: gsComments(gsComment(10, "x", 0))})
	y := gsCycle(2, gsPulls(gsPull(2, "y2"), gsPull(3, "y3")), gsListing{2: gsComments(gsComment(20, "y", 0))})
	sequential := func(first, second githubsync.Cycle) gsView {
		order := &gsEnv{t: e.t, store: newStore(e.t)}
		order.record(first)
		order.record(second)
		return order.view()
	}
	xThenY, yThenX := sequential(x, y), sequential(y, x)
	if reflect.DeepEqual(xThenY, yThenX) {
		e.t.Fatal("fixture: the order of the two cycles must be observable")
	}

	for round := range 3 {
		racing := &gsEnv{t: e.t, store: e.store}
		if round > 0 {
			racing.store = newStore(e.t)
		}
		start := make(chan struct{})
		failures := make([]error, 2)
		var workers sync.WaitGroup
		for i, cycle := range []githubsync.Cycle{x, y} {
			workers.Go(func() {
				<-start
				failures[i] = racing.store.RecordCycle(context.Background(), cycle)
			})
		}
		close(start)
		workers.Wait()
		for _, err := range failures {
			if err != nil {
				e.t.Fatal(err)
			}
		}
		if got := racing.view(); !reflect.DeepEqual(got, xThenY) && !reflect.DeepEqual(got, yThenX) {
			e.t.Fatalf("round %d\n got %+v\nwant %+v\n  or %+v", round+1, got, xThenY, yThenX)
		}
	}
}
