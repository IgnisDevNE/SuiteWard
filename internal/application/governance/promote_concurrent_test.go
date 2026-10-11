package governance_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Two approved proposals share one baseline. There is no admission gate: the
// first promotion wins and the canonical compare-and-set blocks the other until
// it has a new revision against the new canonical and a fresh exact approval.
func TestConcurrentProposalsResolveByCanonicalCompareAndSet(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "first"))
	second := newCandidate(t, "p2", "", protectedOf(t, "second"))
	// The second proposal's revision-2 is bound to version-1. Its evidence is
	// seeded with the proposal because assessments have no write path yet.
	revised := newCandidate(t, "p2", "version-1", protectedOf(t, "second"), "revision-2")
	second.assessments = append(second.assessments, revised.assessments...)
	w := newWorld(t, first, second)
	for _, c := range []candidate{first, second} {
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

	revision := w.revise(revised.proposal.Current())
	if !revision.Committed || revision.Current != second.reference("revision-2") {
		t.Fatalf("revise = %+v, want a committed revision-2", revision)
	}
	w.requireWrites(before, 1)

	unapproved := w.promote(revised.mergedRequest(t, "promote-3", "version-2"))
	if unapproved.Outcome != contract.PromotionBlocked || unapproved.Reason != contract.PromotionReasonApprovalMissing || unapproved.Committed {
		t.Fatalf("promotion of revision-2 without approval = %+v, want an uncommitted block for a missing approval", unapproved)
	}
	w.requireWrites(before, 1)

	approval := w.consent(w.command(revised, "revision-2", "approve-p2-r2", "comment-p2-r2", contract.ApproveConsent, 1))
	if !approval.Receipt.CurrentApprovalEligible {
		t.Fatalf("approval of revision-2 is not eligible: %v", approval.Receipt.Result.Reason())
	}
	retried := w.promote(revised.mergedRequest(t, "promote-3", "version-2"))
	if retried.Outcome != contract.PromotionProposed || !retried.Committed {
		t.Fatalf("promotion of the approved revision-2 = %+v, want a committed promotion", retried)
	}
	if w.currentVersion() != "version-2" {
		t.Fatalf("current version = %q, want version-2", w.currentVersion())
	}
}
