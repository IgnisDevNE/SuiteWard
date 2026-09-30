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

func TestProposalRevisePreservesHistoryAndRejectsSupersededResolution(t *testing.T) {
	first := mustProposalRevision(t, proposalBindingInput(), "source-1", "carrier-1")
	original := mustProposal(t, first)
	input := proposalBindingInput()
	input.Reference.RevisionID = "revision-2"
	input.CoveredInputs["runner"] = "runner-2"
	second := mustProposalRevision(t, input, "source-2", first.Carrier())
	revised, err := original.Revise(second)
	if err != nil {
		t.Fatal(err)
	}
	if !revised.Current().Binding().Equal(second.Binding()) || revised.Current().Origin() != second.Origin() {
		t.Fatal("new revision did not become the exact current snapshot")
	}
	if !original.Current().Binding().Equal(first.Binding()) {
		t.Fatal("revision changed the receiver's immutable snapshot")
	}
	if _, err := original.Lookup(second.Binding().Reference(), second.Carrier()); !errors.Is(err, contract.ErrUnknownRevision) {
		t.Fatalf("original snapshot learned a later revision: %v", err)
	}
	historic, err := revised.Lookup(first.Binding().Reference(), first.Carrier())
	if err != nil || !historic.Binding().Equal(first.Binding()) || historic.Origin() != first.Origin() {
		t.Fatalf("supersession rewrote or discarded history: %v", err)
	}
	if got, err := revised.Resolve(first.Binding().Reference(), first.Carrier()); !errors.Is(err, contract.ErrSupersededRevision) || !got.IsZero() {
		t.Fatalf("superseded reference resolved implicitly: %v", err)
	}
	if got, err := revised.Resolve(second.Binding().Reference(), second.Carrier()); err != nil || !got.Binding().Equal(second.Binding()) {
		t.Fatalf("current exact revision did not resolve: %v", err)
	}
}

func TestProposalReviseRejectsInvalidContextAndReusedIDs(t *testing.T) {
	first := mustProposalRevision(t, proposalBindingInput(), "source-1", "carrier-1")
	initial := mustProposal(t, first)
	input := proposalBindingInput()
	input.Reference.RevisionID = "revision-2"
	second := mustProposalRevision(t, input, "source-2", first.Carrier())
	proposal, err := initial.Revise(second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*contract.BindingInput, *contract.ApprovalCarrierID)
		want   error
	}{
		{"foreign project", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { i.Reference.ProjectID = "project-2" }, contract.ErrProposalContextMismatch},
		{"foreign suite", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { i.Reference.SuiteID = "suite-2" }, contract.ErrProposalContextMismatch},
		{"foreign proposal", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { i.Reference.ProposalID = "proposal-2" }, contract.ErrProposalContextMismatch},
		{"foreign carrier", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { *c = "carrier-2" }, contract.ErrProposalContextMismatch},
		{"current ID", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { i.Reference.RevisionID = "revision-2" }, contract.ErrRevisionExists},
		{"historical ID", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) { i.Reference.RevisionID = "revision-1" }, contract.ErrRevisionExists},
		{"rebound historical ID", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) {
			i.Reference.RevisionID = "revision-1"
			i.CoveredInputs["runner"] = "replacement"
		}, contract.ErrRevisionExists},
		{"context before reuse", func(i *contract.BindingInput, c *contract.ApprovalCarrierID) {
			i.Reference.ProjectID = "project-2"
			i.Reference.RevisionID = "revision-1"
		}, contract.ErrProposalContextMismatch},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			input := proposalBindingInput()
			input.Reference.RevisionID = "revision-3"
			carrier := first.Carrier()
			tt.change(&input, &carrier)
			next := mustProposalRevision(t, input, "next-source", carrier)
			got, err := proposal.Revise(next)
			if !errors.Is(err, tt.want) || !got.IsZero() {
				t.Fatalf("invalid revision returned present=%v error=%v, want %v", !got.IsZero(), err, tt.want)
			}
			if !proposal.Current().Binding().Equal(second.Binding()) {
				t.Fatal("rejected revision changed the receiver")
			}
		})
	}
	if got, err := initial.Revise(first); !errors.Is(err, contract.ErrRevisionExists) || !got.IsZero() {
		t.Fatalf("identical revision ID was recycled: %v", err)
	}
	if got, err := initial.Revise(contract.ProposalRevision{}); !errors.Is(err, contract.ErrInvalidProposal) || !got.IsZero() {
		t.Fatalf("absent next revision was accepted: %v", err)
	}
	var absent contract.Proposal
	if got, err := absent.Revise(first); !errors.Is(err, contract.ErrInvalidProposal) || !got.IsZero() {
		t.Fatalf("absent receiver accepted a revision: %v", err)
	}
}

func TestProposalRevisionBranchesDoNotShareMutableHistory(t *testing.T) {
	proposal := mustProposal(t, mustProposalRevision(t, proposalBindingInput(), "source", "carrier"))
	for _, id := range []contract.ProposalRevisionID{"revision-2", "revision-3"} {
		input := proposalBindingInput()
		input.Reference.RevisionID = id
		var err error
		proposal, err = proposal.Revise(mustProposalRevision(t, input, "source", "carrier"))
		if err != nil {
			t.Fatal(err)
		}
	}
	input := proposalBindingInput()
	input.Reference.RevisionID = "revision-4-a"
	firstBranch, err := proposal.Revise(mustProposalRevision(t, input, "source-a", "carrier"))
	if err != nil {
		t.Fatal(err)
	}
	input.Reference.RevisionID = "revision-4-b"
	secondBranch, err := proposal.Revise(mustProposalRevision(t, input, "source-b", "carrier"))
	if err != nil {
		t.Fatal(err)
	}
	if firstBranch.Current().Binding().Reference().RevisionID != "revision-4-a" || secondBranch.Current().Binding().Reference().RevisionID != "revision-4-b" || proposal.Current().Binding().Reference().RevisionID != "revision-3" {
		t.Fatal("forked revisions modified another immutable history snapshot")
	}
}
