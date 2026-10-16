package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var mgSHA4 = strings.Repeat("4e", 20)

// newMgBootWorld is a repository whose Suite has no canonical and whose default branch has no protected files, so the
// first canonical can only come from the merge of a pull request: pull request 5 carries the approved bootstrap of
// its head (the protected files of "v1").
func newMgBootWorld(t *testing.T) *mgWorld {
	t.Helper()
	cj := newCJWorld(t, false)
	cj.reader.repos[bootRepo].trees[bootHead] = &bootTree{files: map[string]string{"src/main.go": "package main\n"}}
	cj.pullProposal(5, cjHead2, bootFiles("v1"))
	cj.approveIn(bootRepoID, 201, 1, 5)
	cj.sync(map[int]string{4: cjHead3, 5: cjHead2})
	return newMgWorldOf(t, cj)
}

func TestMergesPromoteTheFirstCanonicalFromTheMergeOfTheBootstrapCarrier(t *testing.T) {
	w := newMgBootWorld(t)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v1", time.Minute)
	w.advance(time.Minute)

	w.mustMerge()

	outcome := w.wantOutcome(5, governance.MergePromoted, "")
	if outcome.Version != "V1" || outcome.Source != contract.SourceRevision(mgSHA5) || outcome.Reference == nil || outcome.Reference.ProposalID != "P1" {
		t.Fatalf("outcome %+v, want V1 from the merge commit under P1", outcome)
	}
	if version, found := w.canonical(); !found || version != "V1" {
		t.Fatalf("canonical %q (found %v), want V1", version, found)
	}
	if got := w.mergeKeys(); !slices.Equal(got, []string{"merge:1001:5"}) {
		t.Fatalf("merge messages %v, want one for pull 5", got)
	}
	payload := w.payload("merge:1001:5")
	if payload["kind"] != "promoted" || payload["version"] != "V1" || payload["source"] != mgSHA5 || payload["reference"] != "P1-R1" {
		t.Fatalf("payload %v, want the promotion of V1 from the merge commit", payload)
	}
	if !w.processed(5) {
		t.Fatal("the merge is not marked processed")
	}
	w.wantProblem("")
}

func TestMergesBlockTheBootstrapMergeWhoseTreeDiffersWithoutAVersion(t *testing.T) {
	w := newMgBootWorld(t)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute) // the merge commit holds other protected files than the approved head
	w.advance(time.Minute)

	w.mustMerge()

	outcome := w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonDiffersFromApproved)
	if outcome.Version != "" {
		t.Fatalf("outcome %+v, want no version: no canonical exists to leave in force", outcome)
	}
	if _, found := w.canonical(); found {
		t.Fatal("a canonical exists after a blocked bootstrap merge")
	}
	if got := w.mergeKeys(); !slices.Equal(got, []string{"merge:1001:5"}) {
		t.Fatalf("merge messages %v, want one for pull 5", got)
	}
	payload := w.payload("merge:1001:5")
	if version, present := payload["version"]; !present || version != "" || payload["kind"] != "blocked" || payload["reason"] != "differs-from-approved" {
		t.Fatalf("payload %v, want a blocked merge with the empty version", payload)
	}
	if !w.processed(5) {
		t.Fatal("the merge is not marked processed")
	}
}

func TestMergesRecordAnEarlierNonCarrierMergeBeforeCanonicalAndGoOn(t *testing.T) {
	w := newMgBootWorld(t)
	w.leave(4, 5)
	w.merged(4, cjHead3, mgSHA4, "v1", time.Minute) // merged first, with no bootstrap proposal of its own
	w.merged(5, cjHead2, mgSHA5, "v1", 2*time.Minute)
	w.advance(3 * time.Minute)

	w.mustMerge()

	if outcome := w.wantOutcome(4, governance.MergeBeforeCanonical, ""); outcome.Version != "" {
		t.Fatalf("outcome %+v, want no version", outcome)
	}
	w.wantOutcome(5, governance.MergePromoted, "")
	if got := w.mergeKeys(); !slices.Equal(got, []string{"merge:1001:5"}) {
		t.Fatalf("merge messages %v, want only the promotion", got)
	}
	if !w.processed(4) || !w.processed(5) {
		t.Fatal("a merge is not marked processed")
	}
}

func TestMergesDecideAMergeAfterTheFirstCanonicalAgainstIt(t *testing.T) {
	w := newMgBootWorld(t)
	w.sync(map[int]string{4: cjHead3, 5: cjHead2, 6: cjHead4})
	w.leave(5, 6)
	w.merged(5, cjHead2, mgSHA5, "v1", time.Minute)
	w.merged(6, cjHead4, mgSHA6, "v2", 2*time.Minute) // protected files that no proposal covers
	w.advance(3 * time.Minute)

	w.mustMerge()

	w.wantOutcome(5, governance.MergePromoted, "")
	if outcome := w.wantOutcome(6, governance.MergeBlocked, governance.MergeReasonUnapprovedChange); outcome.Version != "V1" {
		t.Fatalf("outcome %+v, want the canonical V1 left in force: the merge is decided against V1, not before it", outcome)
	}
}

func TestMergesNeverRefuseWithIntegrityFailedWithoutACanonical(t *testing.T) {
	w := newMgBootWorld(t)
	if err := w.verdicts.Record(t.Context(), integrity.Record{Project: w.project(), Suite: "default", Version: "V1", Verdict: integrity.Failed, CheckedAt: w.clock}); err != nil {
		t.Fatal(err)
	}
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v1", time.Minute)
	w.advance(time.Minute)

	w.mustMerge()

	w.wantOutcome(5, governance.MergePromoted, "")
}

func TestMergesDoNothingForARepositoryWithoutASuite(t *testing.T) {
	cj := newCJWorld(t, false)
	cj.sync(map[int]string{4: cjHead3})
	w := newMgWorldOf(t, cj)
	w.leave(4)
	w.merged(4, cjHead3, mgSHA4, "v1", time.Minute)
	w.advance(time.Minute)

	w.mustMerge()

	if w.processed(4) {
		t.Fatal("a merge was marked processed although the Suite does not exist yet")
	}
}
