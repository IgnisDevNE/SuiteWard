package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestExistingBaselineBootstrapEstablishesFirstCanonical(t *testing.T) {
	w, c := promotionWorld(t)
	request := c.baselineRequest(t, "bootstrap-1", "version-1")
	before := w.revision()

	result := w.bootstrap(request)

	if result.Outcome != contract.PromotionProposed || !result.Committed || result.Duplicate {
		t.Fatalf("result = %+v, want a committed proposed bootstrap", result)
	}
	w.requireWrites(before, 1)
	if w.currentVersion() != "version-1" {
		t.Fatalf("current version = %q, want version-1", w.currentVersion())
	}
	if receipt, found := w.receipt("bootstrap-1"); !found || receipt.Kind != governance.OperationBootstrap {
		t.Fatalf("receipt = %+v, found %v", receipt, found)
	}
	replay := w.bootstrap(request)
	if !replay.Duplicate || !replay.Committed || replay.Record.VersionID() != "version-1" {
		t.Fatalf("replay = %+v, want the original record as a committed duplicate", replay)
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
		if result.Outcome != contract.PromotionBlocked || result.Reason != contract.PromotionReasonIntegrationMismatch || result.Committed {
			t.Fatalf("result = %+v, want a block for a non-baseline integration", result)
		}
		w.requireWrites(before, 0)
	})

	w.promote(first.mergedRequest(t, "promote-1", "version-1"))

	t.Run("canonical present", func(t *testing.T) {
		before := w.revision()
		result := w.bootstrap(second.baselineRequest(t, "bootstrap-2", "version-2"))
		if result.Outcome != contract.PromotionBlocked || result.Reason != contract.PromotionReasonCanonicalPresent || result.Committed {
			t.Fatalf("result = %+v, want a block because a canonical exists", result)
		}
		w.requireWrites(before, 0)
	})
}

func TestCorrectionCreatesNewVersionAttributingTheCorrectedOneAndKeepsHistory(t *testing.T) {
	v1 := protectedOf(t, "v1")
	first := newCandidate(t, "p1", "", v1)
	second := newCandidate(t, "p2", "version-1", protectedOf(t, "v2"))
	fix := newCandidate(t, "p3", "version-2", v1)
	w := newWorld(t, first, second, fix)
	for _, c := range []candidate{first, second, fix} {
		w.approve(c)
	}
	w.promote(first.mergedRequest(t, "promote-1", "version-1"))
	w.promote(second.mergedRequest(t, "promote-2", "version-2"))
	request := governance.CorrectionRequest{Promotion: fix.mergedRequest(t, "correct-1", "version-3"), TargetVersionID: "version-1"}
	before := w.revision()

	missing := governance.CorrectionRequest{Promotion: request.Promotion, TargetVersionID: "version-9"}
	if _, err := governance.Correct(context.Background(), w.mem, missing); !errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("correction of an unknown version: error = %v, want not found", err)
	}
	w.requireWrites(before, 0)

	result := w.correct(request)

	if result.Outcome != contract.PromotionProposed || !result.Committed || result.Record.CorrectsVersionID() != "version-1" {
		t.Fatalf("result = %+v, want a committed correction of version-1", result)
	}
	w.requireWrites(before, 1)
	if w.currentVersion() != "version-3" {
		t.Fatalf("current version = %q, want version-3", w.currentVersion())
	}
	for _, id := range []contract.SuiteVersionID{"version-1", "version-2", "version-3"} {
		if _, found := w.version(id); !found {
			t.Fatalf("%s is missing from history after the correction", id)
		}
	}
	if receipt, found := w.receipt("correct-1"); !found || receipt.Kind != governance.OperationCorrect {
		t.Fatalf("receipt = %+v, found %v", receipt, found)
	}
	replay := w.correct(request)
	if !replay.Duplicate || !replay.Committed || replay.Record.CorrectsVersionID() != "version-1" {
		t.Fatalf("replay = %+v, want the original correction as a committed duplicate", replay)
	}
	w.requireWrites(before, 1)
}
