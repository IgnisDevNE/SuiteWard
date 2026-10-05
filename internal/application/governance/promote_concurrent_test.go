package governance_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Two approved proposals share one baseline. There is no admission gate: the
// first promotion wins and the canonical compare-and-set blocks the other until
// it is rebased onto the new canonical and approved again.
func TestConcurrentProposalsResolveByCanonicalCompareAndSet(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "first"))
	second := newCandidate(t, "p2", "", protectedOf(t, "second"))
	// A revision needs a store write path that does not exist yet, so a new
	// proposal against version-1 stands in for the rebased second proposal.
	rebased := newCandidate(t, "p2-rebased", "version-1", protectedOf(t, "second"))
	w := newWorld(t, first, second, rebased)
	for _, c := range []candidate{first, second, rebased} {
		w.approve(c)
	}

	won := w.promote(first.mergedRequest(t, "promote-1", "version-1"))
	if won.Outcome != contract.PromotionProposed || !won.Committed {
		t.Fatalf("first promotion = %+v, want a committed promotion", won)
	}

	before := w.revision()
	blocked := w.promote(second.mergedRequest(t, "promote-2", "version-2"))
	if blocked.Outcome != contract.PromotionBlocked || blocked.Reason != contract.PromotionReasonCanonicalChanged || blocked.Committed {
		t.Fatalf("second promotion = %+v, want an uncommitted block for a changed canonical", blocked)
	}
	w.requireWrites(before, 0)
	if _, found := w.receipt("promote-2"); found {
		t.Fatal("a blocked promotion left a receipt")
	}
	if w.currentVersion() != "version-1" {
		t.Fatalf("current version = %q, want version-1", w.currentVersion())
	}

	retried := w.promote(rebased.mergedRequest(t, "promote-3", "version-2"))
	if retried.Outcome != contract.PromotionProposed || !retried.Committed {
		t.Fatalf("rebased promotion = %+v, want a committed promotion", retried)
	}
	if w.currentVersion() != "version-2" {
		t.Fatalf("current version = %q, want version-2", w.currentVersion())
	}
}
