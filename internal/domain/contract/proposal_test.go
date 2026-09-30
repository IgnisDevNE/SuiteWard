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

func TestProposalExactReferenceResolution(t *testing.T) {
	initial := mustProposalRevision(t, proposalBindingInput(), "baseline-source", "carrier-1")
	proposal := mustProposal(t, initial)
	for name, resolve := range map[string]func(contract.ProposalReference, contract.ApprovalCarrierID) (contract.ProposalRevision, error){"lookup": proposal.Lookup, "resolve": proposal.Resolve} {
		t.Run(name, func(t *testing.T) {
			got, err := resolve(initial.Binding().Reference(), initial.Carrier())
			if err != nil || !got.Binding().Equal(initial.Binding()) || got.Origin() != initial.Origin() || got.Carrier() != initial.Carrier() {
				t.Fatalf("exact reference lost its immutable target: %v", err)
			}
		})
	}
}

func TestProposalRejectsMissingUnknownAndForeignReferences(t *testing.T) {
	initial := mustProposalRevision(t, proposalBindingInput(), "source-1", "carrier-1")
	proposal := mustProposal(t, initial)
	cases := []struct {
		name   string
		change func(*contract.ProposalReference, *contract.ApprovalCarrierID)
		want   error
	}{
		{"omitted", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { *r = contract.ProposalReference{} }, contract.ErrInvalidReference},
		{"missing revision", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.RevisionID = "" }, contract.ErrInvalidReference},
		{"blank revision", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.RevisionID = "\u2003" }, contract.ErrInvalidReference},
		{"missing carrier", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { *c = "" }, contract.ErrInvalidReference},
		{"blank carrier", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { *c = " \t" }, contract.ErrInvalidReference},
		{"foreign project", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.ProjectID = "project-2" }, contract.ErrProposalContextMismatch},
		{"foreign suite", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.SuiteID = "suite-2" }, contract.ErrProposalContextMismatch},
		{"foreign proposal", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.ProposalID = "proposal-2" }, contract.ErrProposalContextMismatch},
		{"foreign carrier", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { *c = "carrier-2" }, contract.ErrProposalContextMismatch},
		{"unknown revision", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) { r.RevisionID = "revision-99" }, contract.ErrUnknownRevision},
		{"context before existence", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) {
			r.ProjectID = "project-2"
			r.RevisionID = "revision-99"
		}, contract.ErrProposalContextMismatch},
		{"validity before context", func(r *contract.ProposalReference, c *contract.ApprovalCarrierID) {
			r.ProjectID = "project-2"
			r.RevisionID = ""
		}, contract.ErrInvalidReference},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ref, carrier := initial.Binding().Reference(), initial.Carrier()
			tt.change(&ref, &carrier)
			for name, resolve := range map[string]func(contract.ProposalReference, contract.ApprovalCarrierID) (contract.ProposalRevision, error){"lookup": proposal.Lookup, "resolve": proposal.Resolve} {
				got, err := resolve(ref, carrier)
				if !errors.Is(err, tt.want) || !got.IsZero() {
					t.Errorf("%s returned present=%v error=%v, want %v", name, !got.IsZero(), err, tt.want)
				}
			}
		})
	}
	var absent contract.Proposal
	for name, resolve := range map[string]func(contract.ProposalReference, contract.ApprovalCarrierID) (contract.ProposalRevision, error){"lookup": absent.Lookup, "resolve": absent.Resolve} {
		got, err := resolve(initial.Binding().Reference(), initial.Carrier())
		if !errors.Is(err, contract.ErrInvalidProposal) || !got.IsZero() {
			t.Errorf("%s on absent proposal error=%v, want ErrInvalidProposal", name, err)
		}
	}
}
