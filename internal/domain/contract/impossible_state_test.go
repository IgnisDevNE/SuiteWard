package contract

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// These tests build values no public constructor produces, to prove that an impossible state is an error and not a
// decision: a command NewCommand would refuse, and evidence NewIntegrityEvidence would refuse.

func impossibleStateBinding(t *testing.T) ApprovalBinding {
	t.Helper()
	binding, err := NewApprovalBinding(BindingInput{
		Reference: ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"},
		Manifest:  artifact.Hash([]byte("manifest")), Scope: artifact.Hash([]byte("scope")), PolicyRevision: "policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestApplyRefusesACommandWhoseReferenceTheProposalCannotAnswer(t *testing.T) {
	binding := impossibleStateBinding(t)
	revision, err := NewProposalRevision(binding, "origin", OriginPull, "carrier")
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewPrincipal("owner", Human)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy("project", "policy", owner)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := NewConsent("project", "suite", "proposal")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []ConsentAction{ApproveConsent, RevokeConsent} {
		// A blank carrier is not a mismatch of context: it is a command that cannot exist.
		command := Command{input: CommandInput{OperationID: "op", SourceCommandID: "src", Actor: owner, Reference: binding.Reference(), Carrier: " ", Action: action, Order: 1}}
		next, _, err := consent.Apply(proposal, policy, command)
		if !errors.Is(err, ErrInvalidConsent) || len(next.Results()) != 0 {
			t.Fatalf("action %d: error %v with %d results; want ErrInvalidConsent and no history", action, err, len(next.Results()))
		}
	}
}

func TestAssessIntegrityRefusesEvidenceWithAnUnknownOutcome(t *testing.T) {
	binding := impossibleStateBinding(t)
	evidence := IntegrityEvidence{emitter: "emitter", source: "source", binding: binding, outcome: 99}
	assessment, err := AssessIntegrity("source", binding, &evidence)
	if !errors.Is(err, ErrInvalidIntegrityAssessment) || assessment.Reason() != 0 {
		t.Fatalf("assessment reason %d, error %v; want ErrInvalidIntegrityAssessment", assessment.Reason(), err)
	}
}
