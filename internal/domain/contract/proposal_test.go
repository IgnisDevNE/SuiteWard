package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func mustProposalRevision(t *testing.T, input contract.BindingInput, origin contract.SourceRevision, carrier contract.ApprovalCarrierID) contract.ProposalRevision {
	t.Helper()
	revision, err := contract.NewProposalRevision(mustBinding(t, input), origin, carrier)
	if err != nil {
		t.Fatalf("NewProposalRevision(): %v", err)
	}
	return revision
}

func mustProposal(t *testing.T, initial contract.ProposalRevision) contract.Proposal {
	t.Helper()
	proposal, err := contract.NewProposal(initial)
	if err != nil {
		t.Fatalf("NewProposal(): %v", err)
	}
	return proposal
}

func TestProposalRevisionPreservesBindingOriginAndCarrier(t *testing.T) {
	input := proposalBindingInput()
	binding := mustBinding(t, input)
	revision, err := contract.NewProposalRevision(binding, " baseline-source ", " different-pr-conversation ")
	if err != nil {
		t.Fatal(err)
	}
	if revision.IsZero() || !revision.Binding().Equal(binding) || revision.Origin() != " baseline-source " || revision.Carrier() != " different-pr-conversation " {
		t.Fatal("revision lost its exact binding, separate origin, or carrier")
	}
	input.CoveredInputs["runner"] = "changed"
	returned := revision.Binding().CoveredInputs()
	returned["runner"] = "also changed"
	if !revision.Binding().Equal(mustBinding(t, proposalBindingInput())) {
		t.Fatal("retained or returned inputs mutated an immutable proposal revision")
	}
	proposal := mustProposal(t, revision)
	if proposal.IsZero() || !proposal.Current().Binding().Equal(binding) || proposal.Current().Origin() != revision.Origin() || proposal.Current().Carrier() != revision.Carrier() {
		t.Fatal("proposal did not preserve its initial revision")
	}
}

func TestProposalRevisionRejectsIncompleteValues(t *testing.T) {
	binding := mustBinding(t, proposalBindingInput())
	cases := []struct {
		name    string
		binding contract.ApprovalBinding
		origin  contract.SourceRevision
		carrier contract.ApprovalCarrierID
	}{
		{"absent binding", contract.ApprovalBinding{}, "source", "carrier"},
		{"empty origin", binding, "", "carrier"},
		{"blank origin", binding, "\u2003", "carrier"},
		{"empty carrier", binding, "source", ""},
		{"blank carrier", binding, "source", " \t"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			revision, err := contract.NewProposalRevision(tt.binding, tt.origin, tt.carrier)
			if !errors.Is(err, contract.ErrInvalidProposal) || !revision.IsZero() {
				t.Fatalf("invalid revision returned IsZero=%v error=%v, want ErrInvalidProposal", revision.IsZero(), err)
			}
		})
	}
	var absent contract.ProposalRevision
	if !absent.IsZero() {
		t.Fatal("unconstructed revision must be absent")
	}
	proposal, err := contract.NewProposal(absent)
	if !errors.Is(err, contract.ErrInvalidProposal) || !proposal.IsZero() || !proposal.Current().IsZero() {
		t.Fatalf("absent initial revision returned a proposal or error=%v, want ErrInvalidProposal", err)
	}
}

func TestProposalOriginDoesNotImplicitlySelectCoveredInputs(t *testing.T) {
	input := proposalBindingInput()
	first := mustProposalRevision(t, input, "source-1", "carrier")
	second := mustProposalRevision(t, input, "source-2", "carrier")
	if first.Origin() == second.Origin() || !first.Binding().Equal(second.Binding()) {
		t.Fatal("origin provenance must remain separate from explicitly chosen covered inputs")
	}
	input.CoveredInputs["source"] = "source-1"
	coveredFirst := mustProposalRevision(t, input, "source-1", "carrier")
	input.CoveredInputs["source"] = "source-2"
	coveredSecond := mustProposalRevision(t, input, "source-2", "carrier")
	if coveredFirst.Binding().Equal(coveredSecond.Binding()) {
		t.Fatal("an explicitly covered source change must change the binding")
	}
}
