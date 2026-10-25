package governance_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// versionsEnv is a Suite at V<versions>, built by merging one approved change per version above V1. V1 holds the tree
// "v1" and V<i> the tree "c<i>". It also holds the approved change of tree final, which is the next merge to decide.
func versionsEnv(t *testing.T, versions int, final string) (*headEnv, candidate, *fakeTree) {
	t.Helper()
	var changes []candidate
	for i := 2; i <= versions; i++ {
		changes = append(changes, newCandidate(t, fmt.Sprintf("pr%d", i), contract.SuiteVersionID(fmt.Sprintf("V%d", i-1)), treeContract(t, headTree(fmt.Sprintf("c%d", i), "package main\n"))))
	}
	last := newCandidate(t, "last", contract.SuiteVersionID(fmt.Sprintf("V%d", versions)), treeContract(t, headTree(final, "package main\n")))
	e := newHeadEnv(t, append(changes, last)...)
	for i, change := range changes {
		e.approve(change)
		e.merge(change, headTree(fmt.Sprintf("c%d", i+2), "package main\n"), governance.MergePromoted)
	}
	e.approve(last)
	return e, last, headTree(final, "package main\n")
}

// A merge restores an earlier version when its manifest equals the integrated one, whichever earlier version holds it,
// and the version it corrects is the current one. Any other merge is a promotion to the next version.
func TestPromoteMergeCorrectsTheCurrentVersionWhenAnEarlierOneHoldsTheManifest(t *testing.T) {
	for name, tc := range map[string]struct {
		versions int
		final    string
		kind     governance.MergeKind
		version  contract.SuiteVersionID
		corrects contract.SuiteVersionID
	}{
		"only V1 exists":                       {1, "fresh", governance.MergePromoted, "V2", ""},
		"a match at V1":                        {4, "v1", governance.MergeCorrected, "V5", "V4"},
		"a match at the version below current": {4, "c3", governance.MergeCorrected, "V5", "V4"},
		"a match in the middle":                {5, "c3", governance.MergeCorrected, "V6", "V5"},
		"no match":                             {4, "fresh", governance.MergePromoted, "V5", ""},
	} {
		t.Run(name, func(t *testing.T) {
			e, last, tree := versionsEnv(t, tc.versions, tc.final)

			result := e.merge(last, tree, tc.kind)

			if result.Version != tc.version || result.Corrects != tc.corrects {
				t.Fatalf("outcome = %+v, want %s correcting %q", result.MergeOutcome, tc.version, tc.corrects)
			}
			if current := e.currentVersion(); current != tc.version {
				t.Fatalf("current version = %q, want %q", current, tc.version)
			}
		})
	}
}

// A version missing below the current one is a broken history: the merge fails and writes nothing.
func TestPromoteMergeRefusesAVersionMissingBelowTheCurrentOne(t *testing.T) {
	e, last, tree := versionsEnv(t, 3, "fresh")
	before := e.revision()

	_, err := governance.PromoteMerge(context.Background(), withoutVersion{e.mem, "V2"}, e.writer, e.mergeRequest(last, tree))

	if !errors.Is(err, governance.ErrInvalidState) || !strings.Contains(err.Error(), `"V2"`) {
		t.Fatalf("error = %v, want ErrInvalidState naming the missing V2", err)
	}
	e.requireWrites(before, 0)
}

// The earlier versions are read once for the whole decision, not once per version: how many reads a merge makes does not
// depend on how many versions there are.
func TestPromoteMergeReadsTheEarlierVersionsWithoutOneReadPerVersion(t *testing.T) {
	versionReads := func(versions int) int {
		e, last, tree := versionsEnv(t, versions, "fresh")
		uow := faulting(e.mem, "Version", 0) // a failure at call 0 never comes: the wrapper only counts

		if _, err := governance.PromoteMerge(context.Background(), uow, e.writer, e.mergeRequest(last, tree)); err != nil {
			t.Fatalf("merge on %d versions: %v", versions, err)
		}
		return uow.calls
	}

	if few, many := versionReads(2), versionReads(6); many != few {
		t.Fatalf("reads of Tx.Version = %d on 6 versions and %d on 2, want the same number", many, few)
	}
}
