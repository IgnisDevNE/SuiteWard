package githubsynctest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

// gsCycleStartCases are the cycle-start cases of the conformance suite (persistence contract, "Cycle start (M1.7c)").
var gsCycleStartCases = []struct {
	name string
	run  func(*gsEnv)
}{
	{"CycleStartRoundTripsMicrosecondTruncatedAndAnEarlierLaterStartIsStillRecorded", gsCycleStartRoundTrip},
	{"FailureKeepsTheStoredCycleStart", gsFailureKeepsCycleStart},
	{"RecordCycleReturnsTheIdsOfTheCommentsInsertedForTheFirstTime", gsFirstTimeCommentIDs},
	{"ConcurrentCyclesCarryingOneNewCommentReturnItsIdExactlyOnce", gsConcurrentFirstTimeCommentID},
}

func gsCycleStartRoundTrip(e *gsEnv) {
	cycle := gsCycle(1, gsPulls(gsPull(1, "a")), nil)
	cycle.StartedAt = gsStart(1).Add(789 * time.Nanosecond) // PostgreSQL keeps microseconds
	e.record(cycle)
	if got := e.view().Repository.CycleStartedAt; !got.Equal(gsStart(1)) {
		e.t.Fatalf("CycleStartedAt = %v, want %v (truncated to the microsecond)", got, gsStart(1))
	}

	earlier := gsCycle(2, gsPulls(gsPull(1, "a")), nil)
	earlier.StartedAt = gsStart(1).Add(-time.Hour) // the value is the cycle's own: a later cycle may start earlier
	e.record(earlier)
	if got := e.view().Repository.CycleStartedAt; !got.Equal(earlier.StartedAt) {
		e.t.Fatalf("CycleStartedAt = %v after a cycle that started earlier, want %v", got, earlier.StartedAt)
	}
}

// A repository whose first record is a failure has no start: the same read a row from before the column gives.
func gsFailureKeepsCycleStart(e *gsEnv) {
	e.fail(githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(1), Kind: githubsync.FailureTransport})
	if got := e.view().Repository.CycleStartedAt; !got.IsZero() {
		e.t.Fatalf("CycleStartedAt = %v for a repository that never completed a cycle, want zero", got)
	}

	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil))
	e.fail(githubsync.Failure{Repo: gsRepo, InstallationID: gsInstallation, At: gsTime(3), Kind: githubsync.FailureNotFound, Status: 404})
	got := e.view().Repository
	if !got.CycleStartedAt.Equal(gsStart(2)) || got.LastError != githubsync.FailureNotFound {
		e.t.Fatalf("repository %+v after a failure, want the failure and CycleStartedAt %v kept", got, gsStart(2))
	}
}

func (e *gsEnv) newComments(cycle githubsync.Cycle) []int64 {
	e.t.Helper()
	ids, err := e.store.RecordCycle(context.Background(), cycle)
	e.must(err)
	return ids
}

func gsFirstTimeCommentIDs(e *gsEnv) {
	pulls := gsPulls(gsPull(1, "a"), gsPull(2, "b"))
	listing := gsListing{1: gsComments(gsComment(12, "x", 0), gsComment(10, "x", 0)), 2: gsComments(gsComment(11, "x", 0))}
	require := func(step string, got, want []int64) {
		e.t.Helper()
		if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
			e.t.Fatalf("%s returned %v, want %v", step, got, want)
		}
	}

	require("the first cycle", e.newComments(gsCycle(1, pulls, listing)), []int64{10, 11, 12})
	require("an identical cycle", e.newComments(gsCycle(2, pulls, listing)), nil)

	edited := gsListing{1: gsComments(gsComment(12, "x", 0), gsComment(10, "edited", 1)), 2: gsComments(gsComment(11, "x", 0))}
	require("a cycle with an edit of a known comment", e.newComments(gsCycle(3, pulls, edited)), nil)

	added := gsListing{1: gsComments(gsComment(13, "x", 0), gsComment(12, "x", 0), gsComment(10, "edited", 1)), 2: gsComments(gsComment(11, "x", 0))}
	require("a cycle with one new comment", e.newComments(gsCycle(4, pulls, added)), []int64{13})

	require("a cycle that no longer lists a comment", e.newComments(gsCycle(5, pulls, listing)), nil)
	require("a cycle that lists it again", e.newComments(gsCycle(6, pulls, added)), nil)

	other := gsCycleOf(gsOtherRepo, 7, gsPulls(gsPull(1, "o")), gsListing{1: gsComments(gsComment(10, "y", 0))})
	require("the first cycle of another repository with an id the first one has", e.newComments(other), []int64{10})
}

// gsConcurrentFirstTimeCommentID has two cycles of one repository, carrying the same new comment, race: between them
// they return its id exactly once, since one of them stored it first.
func gsConcurrentFirstTimeCommentID(e *gsEnv) {
	pulls, listing := gsPulls(gsPull(1, "a")), gsListing{1: gsComments(gsComment(10, "x", 0))}
	for round := range 5 {
		store := e.store
		if round > 0 {
			store = e.newStore(e.t)
		}
		start := make(chan struct{})
		returned, failures := make([][]int64, 2), make([]error, 2)
		var workers sync.WaitGroup
		for i := range returned {
			workers.Go(func() {
				<-start
				returned[i], failures[i] = store.RecordCycle(context.Background(), gsCycle(i+1, pulls, listing))
			})
		}
		close(start)
		workers.Wait()
		for _, err := range failures {
			e.must(err)
		}
		if got := slices.Concat(returned[0], returned[1]); !slices.Equal(got, []int64{10}) {
			e.t.Fatalf("round %d: the two cycles returned %v and %v; want the id 10 exactly once between them", round+1, returned[0], returned[1])
		}
	}
}
