package githubsynctest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

const gsMergeSHA = "0123456789abcdef0123456789abcdef01234567"

// gsResolution is a resolution of pull number's absence since since, merged at mergedAt GitHub minutes (0: closed
// unmerged), read at resolvedAt, with a head and base that differ from gsPull's.
func gsResolution(number, since, mergedAt, resolvedAt int) githubsync.Resolution {
	r := githubsync.Resolution{Number: number, NotOpenSince: gsTime(since), HeadSHA: "head-merged", HeadRef: "renamed", BaseRef: "develop", BaseSHA: "base-merged",
		HeadRepo: "fork/widgets", ResolvedAt: gsTime(resolvedAt)}
	if mergedAt != 0 {
		r.Merged, r.MergedAt, r.MergeCommitSHA = true, gsTime(mergedAt), gsMergeSHA
	}
	return r
}

// gsClosed is a closed-listing item of pull number, updated at updatedAt, merged at mergedAt (0: closed unmerged).
func gsClosed(number, updatedAt, mergedAt int) githubsync.ClosedPull {
	pull := gsPull(number, "closed-head")
	pull.UpdatedAt, pull.HeadRepo = gsTime(updatedAt), "fork/widgets"
	item := githubsync.ClosedPull{ObservedPull: pull}
	if mergedAt != 0 {
		item.MergedAt = gsTime(mergedAt)
	}
	return item
}

func (e *gsEnv) awaiting() []githubsync.PullState {
	e.t.Helper()
	pulls, err := e.store.AwaitingResolution(context.Background(), gsRepo)
	if err != nil {
		e.t.Fatal(err)
	}
	return pulls
}

func (e *gsEnv) merged() []githubsync.MergedPull {
	e.t.Helper()
	pulls, err := e.store.Merged(context.Background(), gsRepo)
	if err != nil {
		e.t.Fatal(err)
	}
	return pulls
}

func (e *gsEnv) resolve(r githubsync.Resolution) {
	e.t.Helper()
	e.must(e.store.RecordResolution(context.Background(), gsRepo, r))
}

func (e *gsEnv) closed(at int, items ...githubsync.ClosedPull) {
	e.t.Helper()
	e.must(e.store.RecordClosed(context.Background(), gsRepo, items, gsTime(at)))
}

func (e *gsEnv) requireNumbers(got []githubsync.PullState, want ...int) {
	e.t.Helper()
	numbers := []int{}
	for _, pull := range got {
		numbers = append(numbers, pull.Number)
	}
	if !reflect.DeepEqual(numbers, append([]int{}, want...)) {
		e.t.Fatalf("pulls %v, want %v", numbers, want)
	}
}

// gsMergeCases are the merge-facts cases of the conformance suite.
var gsMergeCases = []struct {
	name string
	run  func(*gsEnv)
}{
	{"AbsenceResolutionReopenAndANewAbsence", gsResolutionLifecycle},
	{"ResolutionRefreshesTheHeadBaseAndHeadRepository", gsResolutionRefreshes},
	{"StaleResolutionWritesNothing", gsStaleResolution},
	{"ClosedListingFirstCallOnlySetsTheWatermark", gsClosedFirst},
	{"ClosedListingInsertsOnlyUnseenMergedItemsAtOrAboveTheWatermark", gsClosedInserts},
	{"MergedIsOrderedByMergedAtThenNumberAndCarriesProcessed", gsMergedOrder},
	{"MarkProcessedRejectsAnUnknownPull", gsMarkProcessedUnknown},
	{"HeadRepositoryRoundTripsThroughACycle", gsHeadRepository},
	{"InvalidMergeInputIsRejectedWithoutEffects", gsInvalidMergeInput},
	{"EmptyFirstClosedListingStillSetsTheWatermark", gsClosedEmptyFirst},
	{"BlankHeadRepositoryIsRejectedWithoutEffects", gsBlankHeadRepository},
}

func gsResolutionLifecycle(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil))
	e.requireNumbers(e.awaiting())

	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil)) // pull 2 is absent
	e.requireNumbers(e.awaiting(), 2)

	e.resolve(gsResolution(2, 2, 0, 3)) // closed without a merge
	e.requireNumbers(e.awaiting())
	if got := e.merged(); len(got) != 0 {
		e.t.Fatalf("merged %+v, want none for a closed pull", got)
	}

	e.record(gsCycle(4, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil)) // reopened
	e.requireNumbers(e.awaiting())
	e.record(gsCycle(5, gsPulls(gsPull(1, "a")), nil)) // absent again
	e.requireNumbers(e.awaiting(), 2)
	if got := e.pull(2); got.NotOpenSince != gsTime(5) {
		e.t.Fatalf("pull 2 not open since %v, want %v", got.NotOpenSince, gsTime(5))
	}

	if err := e.store.RecordResolution(context.Background(), gsRepo, gsResolution(2, 2, 0, 6)); !errors.Is(err, githubsync.ErrStaleResolution) {
		e.t.Fatalf("resolution of the earlier absence = %v, want ErrStaleResolution", err)
	}
	e.resolve(gsResolution(2, 5, 40, 6))
	e.requireNumbers(e.awaiting())
	if got := e.merged(); len(got) != 1 || got[0].PullState.Number != 2 {
		e.t.Fatalf("merged %+v, want pull 2", got)
	}
}

func gsResolutionRefreshes(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	e.record(gsCycle(2, nil, nil))
	resolution := gsResolution(1, 2, 40, 3)
	e.resolve(resolution)

	want := githubsync.MergedPull{
		PullState: githubsync.PullState{Number: 1, HeadSHA: "head-merged", HeadRef: "renamed", BaseRef: "develop", BaseSHA: "base-merged", UpdatedAt: gsTime(1),
			FirstSeen: gsTime(1), LastSeen: gsTime(1), NotOpenSince: gsTime(2), HeadRepo: "fork/widgets"},
		Resolution: resolution,
	}
	if got := e.merged(); !reflect.DeepEqual(got, []githubsync.MergedPull{want}) {
		e.t.Fatalf("merged\n got %+v\nwant %+v", got, want)
	}
	if got := e.pull(1); got != want.PullState {
		e.t.Fatalf("pull %+v, want the refreshed %+v", got, want.PullState)
	}
}

func gsStaleResolution(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil)) // pull 2 absent since 2
	before := e.view()
	stale := func(why, repo string, r githubsync.Resolution) {
		e.t.Helper()
		if err := e.store.RecordResolution(context.Background(), repo, r); !errors.Is(err, githubsync.ErrStaleResolution) {
			e.t.Fatalf("%s: error %v, want ErrStaleResolution", why, err)
		}
		e.requireView(e.view(), before)
	}
	stale("another absence", gsRepo, gsResolution(2, 9, 40, 3))
	stale("an open pull", gsRepo, gsResolution(1, 2, 40, 3))
	stale("an unknown pull", gsRepo, gsResolution(3, 2, 40, 3))
	stale("an unknown repository", gsOtherRepo, gsResolution(2, 2, 40, 3))
	if got := e.viewOf(gsOtherRepo); !reflect.DeepEqual(got, gsNothing()) {
		e.t.Fatalf("the unknown repository was created: %+v", got)
	}
	e.requireNumbers(e.awaiting(), 2)
}

func gsClosedFirst(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	before := e.view()
	e.closed(2, gsClosed(5, 20, 19), gsClosed(6, 30, 0))
	e.requireView(e.view(), before)
	e.requireNumbers(e.awaiting())

	// The first call set the watermark to the greatest updated_at, 30: a merged item below it is not new, one at it is.
	e.closed(3, gsClosed(5, 20, 19), gsClosed(7, 30, 29))
	want := before
	want.Pulls = append(append([]githubsync.PullState{}, before.Pulls...), gsInserted(7, 30, 3))
	e.requireView(e.view(), want)
}

func gsClosedInserts(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b")), nil))
	e.record(gsCycle(2, gsPulls(gsPull(1, "a")), nil)) // pull 2 absent, unresolved
	e.closed(3, gsClosed(6, 30, 0))                    // watermark 30
	before := e.view()

	e.closed(4,
		gsClosed(7, 40, 39), // unseen, merged, above the watermark: inserted
		gsClosed(8, 25, 24), // below the watermark: ignored
		gsClosed(9, 30, 29), // at the watermark (the same second as its holder): inserted
		gsClosed(6, 50, 0),  // unmerged: ignored, but it raises the watermark
		gsClosed(1, 45, 44), // an open pull is stored already: ignored
		gsClosed(2, 46, 45), // an absent pull is stored already: ignored
	)
	want := before
	want.Pulls = append(append([]githubsync.PullState{}, before.Pulls...), gsInserted(7, 40, 4), gsInserted(9, 30, 4))
	e.requireView(e.view(), want)
	e.requireNumbers(e.awaiting(), 2, 7, 9)
	if got := e.merged(); len(got) != 0 {
		e.t.Fatalf("merged %+v before the merges job resolved anything", got)
	}

	// The watermark rose to 50 (the unmerged item): 49 is below it and 51 is above.
	e.closed(5, gsClosed(10, 49, 48), gsClosed(11, 51, 50))
	e.requireNumbers(e.awaiting(), 2, 7, 9, 11)
}

func gsMergedOrder(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a"), gsPull(2, "b"), gsPull(3, "c")), nil))
	e.record(gsCycle(2, nil, nil))
	e.resolve(gsResolution(1, 2, 30, 3))
	e.resolve(gsResolution(2, 2, 10, 3))
	e.resolve(gsResolution(3, 2, 10, 3))

	numbers := func() (n []int, processed []bool) {
		for _, pull := range e.merged() {
			n, processed = append(n, pull.PullState.Number), append(processed, pull.Processed)
		}
		return n, processed
	}
	if n, processed := numbers(); !reflect.DeepEqual(n, []int{2, 3, 1}) || !reflect.DeepEqual(processed, []bool{false, false, false}) {
		e.t.Fatalf("merged %v processed %v, want 2 3 1 and none processed", n, processed)
	}
	e.must(e.store.MarkProcessed(context.Background(), gsRepo, 3))
	e.must(e.store.MarkProcessed(context.Background(), gsRepo, 3)) // idempotent
	if n, processed := numbers(); !reflect.DeepEqual(n, []int{2, 3, 1}) || !reflect.DeepEqual(processed, []bool{false, true, false}) {
		e.t.Fatalf("merged %v processed %v, want 2 3 1 and only 3 processed", n, processed)
	}
}

func gsMarkProcessedUnknown(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	for _, number := range []int{2, 0} {
		if err := e.store.MarkProcessed(context.Background(), gsRepo, number); !errors.Is(err, githubsync.ErrInvalidObservation) {
			e.t.Fatalf("MarkProcessed(%d) = %v, want ErrInvalidObservation", number, err)
		}
	}
	if err := e.store.MarkProcessed(context.Background(), gsOtherRepo, 1); !errors.Is(err, githubsync.ErrInvalidObservation) {
		e.t.Fatalf("MarkProcessed in an unknown repository = %v, want ErrInvalidObservation", err)
	}
}

func gsHeadRepository(e *gsEnv) {
	fork, deleted, unobserved := gsPull(1, "a"), gsPull(2, "b"), gsPull(3, "c")
	fork.HeadRepo, deleted.HeadRepo = "fork/widgets", "(deleted)"
	e.record(gsCycle(1, gsPulls(fork, deleted, unobserved), nil))
	heads := func() (heads []string) {
		for _, pull := range e.view().Pulls {
			heads = append(heads, pull.HeadRepo)
		}
		return heads
	}
	if got := heads(); !reflect.DeepEqual(got, []string{"fork/widgets", "(deleted)", ""}) {
		e.t.Fatalf("head repositories %q, want the fork, (deleted) and the unobserved \"\"", got)
	}

	fork.HeadRepo, unobserved.HeadRepo = "acme/widgets", "other/widgets"
	e.record(gsCycle(2, gsPulls(fork, deleted, unobserved), nil))
	if got := heads(); !reflect.DeepEqual(got, []string{"acme/widgets", "(deleted)", "other/widgets"}) {
		e.t.Fatalf("head repositories %q after the next cycle, want it to fill and follow the observation", got)
	}
}

func gsInvalidMergeInput(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	e.record(gsCycle(2, nil, nil))
	before := e.view()
	rejects := func(name string, err error) {
		e.t.Helper()
		if !errors.Is(err, githubsync.ErrInvalidObservation) {
			e.t.Fatalf("%s: error %v, want ErrInvalidObservation", name, err)
		}
		e.requireView(e.view(), before)
	}
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		change func(*githubsync.Resolution)
	}{
		{"pull number", func(r *githubsync.Resolution) { r.Number = 0 }},
		{"no absence", func(r *githubsync.Resolution) { r.NotOpenSince = time.Time{} }},
		{"no resolution time", func(r *githubsync.Resolution) { r.ResolvedAt = time.Time{} }},
		{"merged without merged_at", func(r *githubsync.Resolution) { r.MergedAt = time.Time{} }},
		{"merged_at without merged", func(r *githubsync.Resolution) { r.Merged = false }},
		{"short merge commit", func(r *githubsync.Resolution) { r.MergeCommitSHA = "abc" }},
		{"uppercase merge commit", func(r *githubsync.Resolution) { r.MergeCommitSHA = strings.ToUpper(gsMergeSHA) }},
		{"merge commit of an unmerged pull", func(r *githubsync.Resolution) { *r = gsResolution(1, 2, 0, 3); r.MergeCommitSHA = gsMergeSHA }},
	} {
		r := gsResolution(1, 2, 40, 3)
		test.change(&r)
		rejects("resolution with "+test.name, e.store.RecordResolution(ctx, gsRepo, r))
	}
	rejects("closed listing without a time", e.store.RecordClosed(ctx, gsRepo, nil, time.Time{}))
	rejects("closed pull number", e.store.RecordClosed(ctx, gsRepo, []githubsync.ClosedPull{gsClosed(0, 5, 4)}, gsTime(3)))
	noUpdate := gsClosed(5, 5, 4)
	noUpdate.UpdatedAt = time.Time{}
	rejects("closed pull without updated_at", e.store.RecordClosed(ctx, gsRepo, []githubsync.ClosedPull{noUpdate}, gsTime(3)))
	if err := e.store.RecordClosed(ctx, gsOtherRepo, nil, gsTime(3)); !errors.Is(err, githubsync.ErrInvalidObservation) {
		e.t.Fatalf("closed listing of an unknown repository = %v, want ErrInvalidObservation", err)
	}
	if got := e.viewOf(gsOtherRepo); !reflect.DeepEqual(got, gsNothing()) {
		e.t.Fatalf("the unknown repository was created: %+v", got)
	}

	// A merged pull whose merge commit the bound did not find is valid.
	r := gsResolution(1, 2, 40, 3)
	r.MergeCommitSHA = ""
	e.resolve(r)
}

// An empty first listing is still the first call: the next one inserts a merged item whatever its updated_at.
func gsClosedEmptyFirst(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	before := e.view()
	e.closed(2)
	e.requireView(e.view(), before)

	e.closed(3, gsClosed(5, 1, 1))
	e.requireNumbers(e.awaiting(), 5)
	if got := e.pull(5); got.NotOpenSince != gsTime(3) || got.FirstSeen != gsTime(3) {
		e.t.Fatalf("pull 5 %+v, want it inserted absent since %v", got, gsTime(3))
	}
}

// A head repository is empty (not observed) or a name; a blank one is no observation the stores could tell apart.
func gsBlankHeadRepository(e *gsEnv) {
	e.record(gsCycle(1, gsPulls(gsPull(1, "a")), nil))
	e.record(gsCycle(2, nil, nil))
	before := e.view()
	ctx := context.Background()
	for _, blank := range []string{" ", "\t", " \n "} {
		blankPull := gsPull(2, "b")
		blankPull.HeadRepo = blank
		if _, err := e.store.RecordCycle(ctx, gsCycle(3, gsPulls(blankPull), nil)); !errors.Is(err, githubsync.ErrInvalidObservation) {
			e.t.Fatalf("cycle with head repository %q = %v, want ErrInvalidObservation", blank, err)
		}
		r := gsResolution(1, 2, 40, 3)
		r.HeadRepo = blank
		if err := e.store.RecordResolution(ctx, gsRepo, r); !errors.Is(err, githubsync.ErrInvalidObservation) {
			e.t.Fatalf("resolution with head repository %q = %v, want ErrInvalidObservation", blank, err)
		}
		closed := gsClosed(5, 5, 4)
		closed.HeadRepo = blank
		if err := e.store.RecordClosed(ctx, gsRepo, []githubsync.ClosedPull{closed}, gsTime(3)); !errors.Is(err, githubsync.ErrInvalidObservation) {
			e.t.Fatalf("closed listing with head repository %q = %v, want ErrInvalidObservation", blank, err)
		}
		e.requireView(e.view(), before)
	}
}

// gsInserted is the state of pull number as a closed listing at minute at inserts it: merged, absent since at.
func gsInserted(number, updatedAt, at int) githubsync.PullState {
	return githubsync.PullState{Number: number, HeadSHA: "closed-head", HeadRef: fmt.Sprintf("feature-%d", number), BaseRef: "main", BaseSHA: "base-sha", UpdatedAt: gsTime(updatedAt),
		FirstSeen: gsTime(at), LastSeen: gsTime(at), NotOpenSince: gsTime(at), HeadRepo: "fork/widgets"}
}
