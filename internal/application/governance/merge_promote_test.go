package governance_test

import (
	"context"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestPromoteMergePromotesAnApprovedChangeToTheNextVersion(t *testing.T) {
	e := newHeadEnv(t)
	e.approve(e.other)
	before := e.revision()

	result := e.merge(e.other, headTree("other", "package main\n"), governance.MergePromoted)

	if result.Duplicate || result.Version != "V2" || result.Reference == nil || *result.Reference != e.other.current() {
		t.Fatalf("result = %+v, want a new promotion to V2 of %v", result, e.other.current())
	}
	e.requireWrites(before, 2) // the assessment of the merge commit and the promotion
	state := e.state()
	if current, _ := state.Canonical.Suite().CurrentVersionID(); current != "V2" {
		t.Fatalf("current version = %q, want V2", current)
	}
	if !state.Canonical.Contract().Equal(e.other.protected) {
		t.Fatal("canonical contract is not the merged contract")
	}
	operation := "merge:" + string(e.other.carrier)
	promoted, found := e.version("V2")
	if !found || promoted.Record().VersionID() != "V2" || promoted.Record().OperationID() != contract.OperationID(operation) {
		t.Fatalf("promotion record of V2 = %+v, found %v, want the operation %s", promoted.Record(), found, operation)
	}
	if receipt, found := e.receipt(operation); !found || receipt.Kind != governance.OperationPromote || receipt.Promotion == nil {
		t.Fatalf("receipt = %+v, found %v", receipt, found)
	}
}

func TestBlockedMergeRecordsItsOutcomeAndPromotesNothing(t *testing.T) {
	e := newHeadEnv(t) // the other change was never approved
	before := e.revision()

	result := e.merge(e.other, headTree("other", "package main\n"), governance.MergeBlocked)

	if result.Reason != governance.MergeReasonApprovalMissing {
		t.Fatalf("reason = %q, want the missing approval", result.Reason)
	}
	e.requireWrites(before, 1) // the assessment of the merge commit; nothing else
	if e.currentVersion() != e.first {
		t.Fatalf("current version = %q, want %q kept", e.currentVersion(), e.first)
	}
	if _, found := e.version("V2"); found {
		t.Fatal("a blocked merge wrote a version")
	}
	if _, found := e.receipt("merge:" + string(e.other.carrier)); found {
		t.Fatal("a blocked merge left a promotion receipt")
	}
	if outcome, found, err := governance.MergeOutcomeOf(context.Background(), e.mem, projectID, suiteID, e.other.carrier); err != nil || !found || outcome.Kind != governance.MergeBlocked {
		t.Fatalf("recorded outcome = %+v, found %v, %v, want the block", outcome, found, err)
	}
}

func TestUnchangedMergeRecordsNoChangeAndWritesNoVersion(t *testing.T) {
	e := newHeadEnv(t)
	before := e.revision()

	result := e.merge(e.other, headTree("v1", "package main\n"), governance.MergeNoChange) // the canonical's own content

	if result.Version != e.first {
		t.Fatalf("version = %q, want %q kept", result.Version, e.first)
	}
	e.requireWrites(before, 0)
	if _, found := e.receipt("merge:" + string(e.other.carrier)); found {
		t.Fatal("a no-change merge left a promotion receipt")
	}
	if outcome, found, err := governance.MergeOutcomeOf(context.Background(), e.mem, projectID, suiteID, e.other.carrier); err != nil || !found || outcome.Kind != governance.MergeNoChange {
		t.Fatalf("recorded outcome = %+v, found %v, %v, want no change", outcome, found, err)
	}
}

// A pull request merges once: its replay answers from the recorded outcome and does not compare the request.
func TestMergeReplayReturnsTheRecordedOutcomeWithoutWriting(t *testing.T) {
	e := newHeadEnv(t)
	e.approve(e.other)
	original := e.merge(e.other, headTree("other", "package main\n"), governance.MergePromoted)
	before := e.revision()
	again := e.mergeRequest(e.other, headTree("v3", "package main\n"))
	again.Source = "another-commit"

	replay, err := governance.PromoteMerge(context.Background(), e.mem, e.writer, again)

	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Kind != original.Kind || replay.Version != original.Version || replay.Source != original.Source || *replay.Reference != *original.Reference {
		t.Fatalf("replay = %+v, want the recorded outcome %+v as a duplicate", replay, original.MergeOutcome)
	}
	e.requireWrites(before, 0)
}
