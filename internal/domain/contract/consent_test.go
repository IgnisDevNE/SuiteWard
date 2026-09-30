package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func consentBindingInput() contract.BindingInput {
	return contract.BindingInput{
		Reference: contract.ProposalReference{
			ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1",
		},
		ExpectedCanonical: "v1",
		Manifest:          artifact.Hash([]byte("manifest")),
		Scope:             artifact.Hash([]byte("scope")),
		PolicyRevision:    "policy",
		CoveredInputs:     map[string]string{"runner": "runner-1"},
	}
}

func consentRevisionForTest(t *testing.T, input contract.BindingInput) contract.ProposalRevision {
	t.Helper()
	binding, err := contract.NewApprovalBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := contract.NewProposalRevision(binding, "origin", "carrier")
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func consentProposalForTest(t *testing.T, input contract.BindingInput) contract.Proposal {
	t.Helper()
	proposal, err := contract.NewProposal(consentRevisionForTest(t, input))
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func consentFixture(t *testing.T) (contract.Consent, contract.Proposal, contract.Policy, contract.CommandInput) {
	t.Helper()
	owner := principalForTest(t, "owner", contract.Human)
	policy, err := contract.NewPolicy("project", "policy", owner)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := contract.NewConsent("project", "suite", "proposal")
	if err != nil {
		t.Fatal(err)
	}
	input := contract.CommandInput{
		OperationID: "operation-1", SourceCommandID: "source-1", Actor: owner,
		Reference: consentBindingInput().Reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 10,
	}
	return consent, consentProposalForTest(t, consentBindingInput()), policy, input
}

func consentCommandForTest(t *testing.T, input contract.CommandInput) contract.Command {
	t.Helper()
	command, err := contract.NewCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func applyConsentForTest(t *testing.T, state contract.Consent, proposal contract.Proposal, policy contract.Policy, input contract.CommandInput) (contract.Consent, contract.CommandResult) {
	t.Helper()
	next, result, err := state.Apply(proposal, policy, consentCommandForTest(t, input))
	if err != nil {
		t.Fatal(err)
	}
	return next, result
}

func TestConsentApprovesExactRevisionWithoutMutatingHistory(t *testing.T) {
	empty, proposal, policy, input := consentFixture(t)
	command := consentCommandForTest(t, input)
	approved, result, err := empty.Apply(proposal, policy, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome() != contract.ConsentApproved || result.Reason() != contract.ConsentReasonNone || result.Duplicate() || result.Command() != command || !approved.HasApproval(proposal, policy) {
		t.Fatal("eligible exact command did not produce its own approval outcome and active consent")
	}
	if empty.HasApproval(proposal, policy) || len(empty.Results()) != 0 {
		t.Fatal("applying consent mutated the previous immutable state")
	}
	results := approved.Results()
	if len(results) != 1 || results[0] != result {
		t.Fatal("approval did not retain its original processing outcome")
	}
	results[0] = contract.CommandResult{}
	if approved.Results()[0] != result {
		t.Fatal("a returned result slice mutated retained processing history")
	}
}

func TestConsentRejectsBlankScope(t *testing.T) {
	for _, ids := range [][3]string{
		{"", "suite", "proposal"}, {" \t", "suite", "proposal"},
		{"project", "", "proposal"}, {"project", "\u2003", "proposal"},
		{"project", "suite", ""}, {"project", "suite", "\n"},
	} {
		_, err := contract.NewConsent(contract.ProjectID(ids[0]), contract.SuiteID(ids[1]), contract.ProposalID(ids[2]))
		if !errors.Is(err, contract.ErrInvalidConsent) {
			t.Errorf("NewConsent(%q) error = %v; want ErrInvalidConsent", ids, err)
		}
	}
}

func TestConsentRejectsForeignOrAbsentContextWithoutHistory(t *testing.T) {
	for _, field := range []string{"project", "suite", "proposal"} {
		t.Run("command "+field, func(t *testing.T) {
			state, proposal, policy, input := consentFixture(t)
			switch field {
			case "project":
				input.Reference.ProjectID = "foreign"
			case "suite":
				input.Reference.SuiteID = "foreign"
			case "proposal":
				input.Reference.ProposalID = "foreign"
			}
			next, _, err := state.Apply(proposal, policy, consentCommandForTest(t, input))
			if !errors.Is(err, contract.ErrInvalidConsent) || len(next.Results()) != 0 || next.HasApproval(proposal, policy) {
				t.Fatalf("foreign command error %v; want invalid context without history or approval", err)
			}
		})
	}
	tests := []struct {
		name string
		edit func(*contract.Consent, *contract.Proposal, *contract.Policy, *contract.Command)
		want error
	}{
		{"absent aggregate", func(s *contract.Consent, _ *contract.Proposal, _ *contract.Policy, _ *contract.Command) {
			*s = contract.Consent{}
		}, contract.ErrInvalidConsent},
		{"absent proposal", func(_ *contract.Consent, p *contract.Proposal, _ *contract.Policy, _ *contract.Command) {
			*p = contract.Proposal{}
		}, contract.ErrInvalidConsent},
		{"absent policy", func(_ *contract.Consent, _ *contract.Proposal, p *contract.Policy, _ *contract.Command) {
			*p = contract.Policy{}
		}, contract.ErrInvalidConsent},
		{"absent command", func(_ *contract.Consent, _ *contract.Proposal, _ *contract.Policy, c *contract.Command) {
			*c = contract.Command{}
		}, contract.ErrInvalidCommand},
		{"foreign proposal", func(_ *contract.Consent, p *contract.Proposal, _ *contract.Policy, _ *contract.Command) {
			input := consentBindingInput()
			input.Reference.SuiteID = "foreign"
			*p = consentProposalForTest(t, input)
		}, contract.ErrInvalidConsent},
		{"foreign policy", func(_ *contract.Consent, _ *contract.Proposal, p *contract.Policy, _ *contract.Command) {
			var err error
			*p, err = contract.NewPolicy("foreign", "policy", principalForTest(t, "owner", contract.Human))
			if err != nil {
				t.Fatal(err)
			}
		}, contract.ErrInvalidConsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, proposal, policy, input := consentFixture(t)
			command := consentCommandForTest(t, input)
			tt.edit(&state, &proposal, &policy, &command)
			next, _, err := state.Apply(proposal, policy, command)
			if !errors.Is(err, tt.want) || len(next.Results()) != 0 || next.HasApproval(proposal, policy) {
				t.Fatalf("invalid context error %v; want %v without history or approval", err, tt.want)
			}
		})
	}
}
