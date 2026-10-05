package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func consentWorld(t *testing.T, revisions ...contract.ProposalRevisionID) (*world, candidate) {
	t.Helper()
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"), revisions...)
	return newWorld(t, c), c
}

func TestApprovalOfCurrentRevisionMakesItEligible(t *testing.T) {
	w, c := consentWorld(t)
	before := w.revision()

	response := w.consent(w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1))

	if !response.Committed || response.Duplicate {
		t.Fatalf("response = %+v, want a new committed outcome", response)
	}
	if response.Receipt.Result.Outcome() != contract.ConsentApproved || !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("receipt = %+v, want an eligible approval", response.Receipt)
	}
	if response.Receipt.EvaluatedReference != c.current() || response.Receipt.PolicyRevisionID != policyID {
		t.Fatalf("receipt evaluated %+v under %q", response.Receipt.EvaluatedReference, response.Receipt.PolicyRevisionID)
	}
	w.requireWrites(before, 1)
}

func TestStaleOrSupersededApprovalIsRejectedAndDoesNotCount(t *testing.T) {
	w, c := consentWorld(t, "revision-1", "revision-2")
	for _, tc := range []struct {
		name     string
		revision contract.ProposalRevisionID
		reason   contract.ConsentReason
	}{
		{"superseded", "revision-1", contract.ConsentReasonSupersededRevision},
		{"unknown", "revision-9", contract.ConsentReasonUnknownRevision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := w.consent(w.command(c, tc.revision, "approve-"+tc.name, "comment-"+tc.name, contract.ApproveConsent, 1))
			if response.Receipt.Result.Outcome() != contract.ConsentRejected || response.Receipt.Result.Reason() != tc.reason {
				t.Fatalf("result = %v/%v, want rejected/%v", response.Receipt.Result.Outcome(), response.Receipt.Result.Reason(), tc.reason)
			}
			if response.Receipt.CurrentApprovalEligible {
				t.Fatal("a rejected approval made the current revision eligible")
			}
		})
	}

	// Rejected commands consume no order, so the exact current revision can still be approved.
	response := w.consent(w.command(c, "revision-2", "approve-current", "comment-current", contract.ApproveConsent, 1))
	if !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("approval of the current revision is not eligible: %v", response.Receipt.Result.Reason())
	}
}

func TestRevocationRemovesEligibility(t *testing.T) {
	w, c := consentWorld(t)
	w.approve(c)

	response := w.consent(w.command(c, "revision-1", "revoke-1", "comment-revoke", contract.RevokeConsent, 2))

	if response.Receipt.Result.Outcome() != contract.ConsentRevoked || response.Receipt.CurrentApprovalEligible {
		t.Fatalf("receipt = %+v, want a revocation without eligibility", response.Receipt)
	}
	blocked := w.promote(c.mergedRequest(t, "promote-1", "version-1"))
	if blocked.Outcome != contract.PromotionBlocked || blocked.Reason != contract.PromotionReasonApprovalMissing {
		t.Fatalf("promotion after revocation = %v/%v, want blocked/approval missing", blocked.Outcome, blocked.Reason)
	}
}

func TestReplayByOperationIDReturnsOriginalReceiptWithoutWriting(t *testing.T) {
	w, c := consentWorld(t)
	command := w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1)
	original := w.consent(command)
	before := w.revision()

	replay := w.consent(command)

	if !replay.Duplicate || !replay.Committed || replay.Receipt != original.Receipt {
		t.Fatalf("replay = %+v, want the original receipt as a committed duplicate", replay)
	}
	w.requireWrites(before, 0)
}

func TestSameSourceCommandWithNewOperationRecordsAliasAndReturnsOriginalReceipt(t *testing.T) {
	w, c := consentWorld(t)
	original := w.consent(w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1))
	before := w.revision()
	retry := w.command(c, "revision-1", "approve-retry", "comment-1", contract.ApproveConsent, 1)

	alias := w.consent(retry)

	if !alias.Duplicate || !alias.Committed || alias.Receipt != original.Receipt {
		t.Fatalf("alias = %+v, want the original receipt as a committed duplicate", alias)
	}
	w.requireWrites(before, 1)
	again := w.consent(retry)
	if !again.Duplicate || again.Receipt != original.Receipt {
		t.Fatalf("alias replay = %+v, want the original receipt", again)
	}
	w.requireWrites(before, 1)
}

func TestReusedOperationIDOrReplayedSourceCommandConflicts(t *testing.T) {
	w, c := consentWorld(t)
	w.approve(c)
	before := w.revision()
	agent := must(contract.NewPrincipal("agent-1", contract.Agent))

	for name, command := range map[string]contract.Command{
		"operation id reused for another source command": w.command(c, "revision-1", "approve-p1", "comment-other", contract.RevokeConsent, 2),
		"source command replayed by another actor":       w.commandBy(agent, c, "revision-1", "approve-agent", "comment-p1", contract.ApproveConsent, 2),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: command})
			if !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	w.requireWrites(before, 0)
}
