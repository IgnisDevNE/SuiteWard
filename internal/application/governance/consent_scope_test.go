package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestConsentReceiptNamesThePromotedVersionOnlyForAPromotedReference(t *testing.T) {
	promoted := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	pending := newCandidate(t, "p2", "version-1", protectedOf(t, "v2"))
	w := newWorld(t, promoted, pending)
	w.approve(promoted)
	w.promote(promoted.mergedRequest(t, "promote-1", "version-1"))

	late := w.consent(w.command(promoted, "revision-1", "revoke-p1", "comment-revoke-p1", contract.RevokeConsent, 2))
	if late.Receipt.PromotedVersionID != "version-1" {
		t.Fatalf("command for a promoted reference: promoted version = %q, want version-1", late.Receipt.PromotedVersionID)
	}
	open := w.consent(w.command(pending, "revision-1", "approve-p2", "comment-p2", contract.ApproveConsent, 1))
	if open.Receipt.PromotedVersionID != "" {
		t.Fatalf("command for a pending reference: promoted version = %q, want none", open.Receipt.PromotedVersionID)
	}
}

func TestConsentIdentityIsScopedToSourceCommandActorProposalSuiteAndProject(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	second := newCandidate(t, "p2", "", protectedOf(t, "v2"))
	elsewhere := newCandidateIn(t, projectID, "suite-2", "p1", "", protectedOf(t, "v3"))
	abroad := newCandidateIn(t, "project-2", suiteID, "p1", "", protectedOf(t, "v4"))
	w := newWorld(t, first, second)
	w.addSuite(projectID, "suite-2", elsewhere)
	w.addSuite("project-2", suiteID, abroad)
	w.approve(first)
	agent := must(contract.NewPrincipal("agent-1", contract.Agent))
	before, beforeElsewhere, beforeAbroad := w.revision(), w.revisionIn(projectID, "suite-2"), w.revisionIn("project-2", suiteID)

	for name, command := range map[string]contract.Command{
		"operation id reused for another proposal":     w.command(second, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1),
		"operation id reused in another Suite":         w.command(elsewhere, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1),
		"operation id reused by another actor":         w.commandBy(agent, first, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1),
		"source command recorded for another proposal": w.command(second, "revision-1", "approve-new-1", "comment-p1", contract.ApproveConsent, 1),
		"operation id reused in another project":       w.command(abroad, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1),
		"source command recorded in another project":   w.command(abroad, "revision-1", "approve-new-4", "comment-p1", contract.ApproveConsent, 1),
		"source command recorded in another Suite":     w.command(elsewhere, "revision-1", "approve-new-2", "comment-p1", contract.ApproveConsent, 1),
		"source command recorded for another actor":    w.commandBy(agent, first, "revision-1", "approve-new-3", "comment-p1", contract.ApproveConsent, 2),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: command})
			if !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	w.requireWrites(before, 0)
	if got := w.revisionIn(projectID, "suite-2"); got != beforeElsewhere || w.revisionIn("project-2", suiteID) != beforeAbroad {
		t.Fatalf("a conflicting command wrote to another scope (suite-2 revision %d, want %d)", got, beforeElsewhere)
	}
}

func TestAliasWriteFailureRollsBackAndTheRetrySucceeds(t *testing.T) {
	w, c := consentWorld(t)
	original := w.consent(w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1))
	retry := w.command(c, "revision-1", "approve-retry", "comment-1", contract.ApproveConsent, 1)
	before := w.revision()
	boom := errors.New("alias write failed")
	w.mem.FailNext(boom)

	if _, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: retry}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the write failure", err)
	}

	w.requireWrites(before, 0)
	if _, found := w.receipt("approve-retry"); found {
		t.Fatal("a failed alias write left a receipt")
	}
	if alias := w.consent(retry); !alias.Duplicate || !alias.Committed || alias.Receipt != original.Receipt {
		t.Fatalf("retry = %+v, want the original receipt as a committed duplicate", alias)
	}
	w.requireWrites(before, 1)
}
