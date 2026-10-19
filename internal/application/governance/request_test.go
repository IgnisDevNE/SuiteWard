package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestMalformedRequestsAreRejectedBeforeAnyUnitOfWork(t *testing.T) {
	w, c := promotionWorld(t)
	before := w.revision()
	good := c.baselineRequest(t, "bootstrap-9", "version-9")
	var noUnitOfWork governance.UnitOfWork

	attempts := map[string]func() error{
		"consent without a unit of work": func() error {
			_, err := governance.ProcessConsent(context.Background(), noUnitOfWork, governance.ConsentRequest{Command: w.command(c, "revision-1", "approve-9", "comment-9", contract.ApproveConsent, 1)})
			return err
		},
		"consent without a command": func() error {
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{})
			return err
		},
		"bootstrap without a unit of work": func() error { _, err := governance.Bootstrap(context.Background(), noUnitOfWork, good); return err },
	}
	for name, change := range map[string]func(*governance.PromoteRequest){
		"operation id":      func(r *governance.PromoteRequest) { r.OperationID = " " },
		"project":           func(r *governance.PromoteRequest) { r.Reference.ProjectID = "" },
		"suite":             func(r *governance.PromoteRequest) { r.Reference.SuiteID = "" },
		"proposal":          func(r *governance.PromoteRequest) { r.Reference.ProposalID = "" },
		"revision":          func(r *governance.PromoteRequest) { r.Reference.RevisionID = "" },
		"carrier":           func(r *governance.PromoteRequest) { r.Carrier = "" },
		"contract":          func(r *governance.PromoteRequest) { r.Proposed = contract.ProtectedContract{} },
		"assessment source": func(r *governance.PromoteRequest) { r.AssessmentSource = "" },
	} {
		malformed := good
		change(&malformed)
		attempts["bootstrap without a "+name] = func() error { _, err := governance.Bootstrap(context.Background(), w.mem, malformed); return err }
	}

	for name, attempt := range attempts {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("error = %v, want invalid request", err)
			}
		})
	}
	w.requireWrites(before, 0)
}

func TestUnknownSuiteOrProposalIsNotFound(t *testing.T) {
	w, _ := promotionWorld(t)
	ghost := newCandidate(t, "ghost", "", protectedOf(t, "v1"))
	nowhere := newCandidateIn(t, projectID, "nowhere", "p1", "", protectedOf(t, "v1"))
	before := w.revision()

	for name, attempt := range map[string]func() error{
		"consent for an unknown proposal": func() error {
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: w.command(ghost, "revision-1", "approve-ghost", "comment-ghost", contract.ApproveConsent, 1)})
			return err
		},
		"consent for an unknown Suite": func() error {
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: w.command(nowhere, "revision-1", "approve-nowhere", "comment-nowhere", contract.ApproveConsent, 1)})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); !errors.Is(err, governance.ErrNotFound) {
				t.Fatalf("error = %v, want not found", err)
			}
		})
	}
	w.requireWrites(before, 0)
}
