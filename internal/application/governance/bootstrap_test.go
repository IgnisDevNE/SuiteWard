package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// promotionWorld has one approved candidate.
func promotionWorld(t *testing.T) (*world, candidate) {
	t.Helper()
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	w.approve(c)
	return w, c
}

func TestExistingBaselineBootstrapEstablishesFirstCanonical(t *testing.T) {
	w, c := promotionWorld(t)
	request := c.baselineRequest(t, "bootstrap-1", "version-1")
	before := w.revision()

	result := w.bootstrap(request)

	if result.Outcome != contract.PromotionProposed || result.Duplicate {
		t.Fatalf("result = %+v, want a new proposed bootstrap", result)
	}
	w.requireWrites(before, 1)
	if w.currentVersion() != "version-1" {
		t.Fatalf("current version = %q, want version-1", w.currentVersion())
	}
	if receipt, found := w.receipt("bootstrap-1"); !found || receipt.Kind != governance.OperationBootstrap || receipt.Promotion == nil || receipt.Promotion.Record.VersionID() != "version-1" {
		t.Fatalf("receipt = %+v, found %v", receipt, found)
	}
	replay := w.bootstrap(request)
	if !replay.Duplicate || replay.Outcome != contract.PromotionProposed {
		t.Fatalf("replay = %+v, want the original outcome as a duplicate", replay)
	}
	w.requireWrites(before, 1)
}

func TestBootstrapNeedsAnExistingBaselineAndNoCanonical(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	second := newCandidate(t, "p2", "version-1", protectedOf(t, "v2"))
	w := newWorld(t, first, second)
	w.approve(first)
	w.approve(second)

	t.Run("merged change", func(t *testing.T) {
		before := w.revision()
		result := w.bootstrap(first.mergedRequest(t, "bootstrap-merged", "version-1"))
		if result.Outcome != contract.PromotionBlocked || result.Reason != contract.PromotionReasonIntegrationMismatch {
			t.Fatalf("result = %+v, want a block for a non-baseline integration", result)
		}
		w.requireWrites(before, 0)
	})

	w.bootstrap(first.baselineRequest(t, "bootstrap-1", "version-1"))

	t.Run("canonical present", func(t *testing.T) {
		before := w.revision()
		result := w.bootstrap(second.baselineRequest(t, "bootstrap-2", "version-2"))
		if result.Outcome != contract.PromotionBlocked || result.Reason != contract.PromotionReasonCanonicalPresent {
			t.Fatalf("result = %+v, want a block because a canonical exists", result)
		}
		w.requireWrites(before, 0)
	})
}

// A Suite whose canonical moved twice (a merged change, then a correction) still answers the bootstrap that made V1
// from its receipt: a replay never decides again.
func TestBootstrapReplayAnswersFromItsReceiptAfterTheCanonicalMoved(t *testing.T) {
	second := newCandidate(t, "pr2", "V1", treeContract(t, headTree("v2", "package main\n")))
	fix := newCandidate(t, "fix", "V2", treeContract(t, headTree("v1", "package main\n"))) // restores the contract of V1 on top of V2
	e := newHeadEnv(t, second, fix)
	e.approve(second)
	e.approve(fix)
	e.merge(second, headTree("v2", "package main\n"), governance.MergePromoted)
	if corrected := e.merge(fix, headTree("v1", "package main\n"), governance.MergeCorrected); corrected.Version != "V3" || corrected.Corrects != "V2" {
		t.Fatalf("correction = %+v, want V3 correcting V2", corrected.MergeOutcome)
	}
	original, found := e.receipt(string(e.boot.OperationID))
	if !found || original.Promotion == nil {
		t.Fatalf("receipt of the bootstrap = %+v, found %v", original, found)
	}
	first, _ := e.version("V1")
	before := e.revision()

	replay := e.bootstrap(e.boot)

	if !replay.Duplicate || replay.Outcome != contract.PromotionProposed {
		t.Fatalf("replay = %+v, want the original outcome as a duplicate", replay)
	}
	e.requireWrites(before, 0)
	if stored, found := e.receipt(string(e.boot.OperationID)); !found || stored.Kind != governance.OperationBootstrap || !stored.Promotion.Record.Equal(original.Promotion.Record) {
		t.Fatalf("receipt after the replay = %+v, found %v, want the original", stored, found)
	}
	if now, found := e.version("V1"); !found || !sameHistory(first, now) {
		t.Fatal("V1 changed or went missing")
	}
	if e.currentVersion() != "V3" {
		t.Fatalf("current version = %q, want V3 kept", e.currentVersion())
	}
	changed := e.boot
	changed.NewVersionID = "V9"
	if _, err := governance.Bootstrap(context.Background(), e.mem, changed); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("a changed request under the same operation: error = %v, want operation conflict", err)
	}
	e.requireWrites(before, 0)
}

// The identity of a recorded bootstrap is its whole request, and its operation id belongs to its kind.
func TestBootstrapOperationIDReusedForAnotherRequestConflicts(t *testing.T) {
	second := newCandidate(t, "pr2", "V1", treeContract(t, headTree("v2", "package main\n")))
	e := newHeadEnv(t, second)
	e.approve(second)
	e.merge(second, headTree("v2", "package main\n"), governance.MergePromoted)
	before := e.revision()
	otherIntegration := must(contract.NewIntegration(projectID, target, "pinned-other", "", contract.IntegrationExistingBaseline))

	for name, change := range map[string]func(*governance.PromoteRequest){
		"revision":          func(r *governance.PromoteRequest) { r.Reference.RevisionID = "R2" },
		"proposal":          func(r *governance.PromoteRequest) { r.Reference.ProposalID = "p-other" },
		"carrier":           func(r *governance.PromoteRequest) { r.Carrier = "carrier-other" },
		"assessment source": func(r *governance.PromoteRequest) { r.AssessmentSource = "pinned-other" },
		"integration":       func(r *governance.PromoteRequest) { r.Integration = otherIntegration },
		"version id":        func(r *governance.PromoteRequest) { r.NewVersionID = "V9" },
		"contract":          func(r *governance.PromoteRequest) { r.Proposed = protectedOf(t, "other") },
		"a merge receipt": func(r *governance.PromoteRequest) {
			r.OperationID = contract.OperationID("merge:" + string(second.carrier))
		},
		"a consent command": func(r *governance.PromoteRequest) { r.OperationID = "approve-pr2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := e.boot
			change(&changed)
			if _, err := governance.Bootstrap(context.Background(), e.mem, changed); !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	for name, id := range map[string]contract.OperationID{"a bootstrap": e.boot.OperationID, "a merge": contract.OperationID("merge:" + string(second.carrier))} {
		t.Run("a consent command under the id of "+name, func(t *testing.T) {
			command := e.command(second, "revision-1", string(id), "comment-other", contract.ApproveConsent, 2)
			if _, err := governance.ProcessConsent(context.Background(), e.mem, governance.ConsentRequest{Command: command}); !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	e.requireWrites(before, 0)
}

func TestBootstrapOfAnUnknownProposalOrSuiteIsNotFound(t *testing.T) {
	w, _ := promotionWorld(t)
	ghost := newCandidate(t, "ghost", "", protectedOf(t, "v1"))
	nowhere := newCandidateIn(t, projectID, "nowhere", "p1", "", protectedOf(t, "v1"))
	before := w.revision()

	for name, request := range map[string]governance.PromoteRequest{
		"proposal": ghost.baselineRequest(t, "bootstrap-ghost", "version-1"),
		"suite":    nowhere.baselineRequest(t, "bootstrap-nowhere", "version-1"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := governance.Bootstrap(context.Background(), w.mem, request); !errors.Is(err, governance.ErrNotFound) {
				t.Fatalf("error = %v, want not found", err)
			}
		})
	}
	w.requireWrites(before, 0)
}
