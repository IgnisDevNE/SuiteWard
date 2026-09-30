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

func TestConsentRejectsIneligibleApprovalsWithRecordedReasons(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*contract.CommandInput, *contract.Policy)
		reason contract.ConsentReason
	}{
		{"unregistered human", func(i *contract.CommandInput, _ *contract.Policy) {
			i.Actor = principalForTest(t, "repo-admin", contract.Human)
		}, contract.ConsentReasonUnauthorized},
		{"agent using owner identity", func(i *contract.CommandInput, _ *contract.Policy) {
			i.Actor = principalForTest(t, "owner", contract.Agent)
		}, contract.ConsentReasonUnauthorized},
		{"service using owner identity", func(i *contract.CommandInput, _ *contract.Policy) {
			i.Actor = principalForTest(t, "owner", contract.Service)
		}, contract.ConsentReasonUnauthorized},
		{"unknown revision", func(i *contract.CommandInput, _ *contract.Policy) { i.Reference.RevisionID = "unknown" }, contract.ConsentReasonUnknownRevision},
		{"wrong carrier", func(i *contract.CommandInput, _ *contract.Policy) { i.Carrier = "another-carrier" }, contract.ConsentReasonContextMismatch},
		{"different governing revision", func(_ *contract.CommandInput, p *contract.Policy) {
			var err error
			*p, err = contract.NewPolicy("project", "new-policy", principalForTest(t, "owner", contract.Human))
			if err != nil {
				t.Fatal(err)
			}
		}, contract.ConsentReasonPolicyMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, proposal, policy, input := consentFixture(t)
			tt.edit(&input, &policy)
			next, result := applyConsentForTest(t, state, proposal, policy, input)
			if result.Outcome() != contract.ConsentRejected || result.Reason() != tt.reason || result.Duplicate() || next.HasApproval(proposal, policy) {
				t.Errorf("outcome %d reason %d; want rejection reason %d without approval", result.Outcome(), result.Reason(), tt.reason)
			}
			if len(next.Results()) != 1 || next.Results()[0] != result || len(state.Results()) != 0 {
				t.Error("in-scope rejection was not retained immutably")
			}
		})
	}
}

func TestConsentEligibilityRequiresExactCurrentFacts(t *testing.T) {
	empty, proposal, policy, input := consentFixture(t)
	approved, _ := applyConsentForTest(t, empty, proposal, policy, input)
	changes := []struct {
		name string
		edit func(*contract.BindingInput)
	}{
		{"project", func(i *contract.BindingInput) { i.Reference.ProjectID = "other-project" }},
		{"suite", func(i *contract.BindingInput) { i.Reference.SuiteID = "other-suite" }},
		{"proposal", func(i *contract.BindingInput) { i.Reference.ProposalID = "other-proposal" }},
		{"revision", func(i *contract.BindingInput) { i.Reference.RevisionID = "r2" }},
		{"baseline", func(i *contract.BindingInput) { i.ExpectedCanonical = "v2" }},
		{"absent baseline", func(i *contract.BindingInput) { i.ExpectedCanonical = "" }},
		{"manifest", func(i *contract.BindingInput) { i.Manifest = artifact.Hash([]byte("changed-manifest")) }},
		{"scope", func(i *contract.BindingInput) { i.Scope = artifact.Hash([]byte("changed-scope")) }},
		{"policy", func(i *contract.BindingInput) { i.PolicyRevision = "policy-2" }},
		{"covered inputs", func(i *contract.BindingInput) { i.CoveredInputs["runner"] = "runner-2" }},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			binding := consentBindingInput()
			tt.edit(&binding)
			if approved.HasApproval(consentProposalForTest(t, binding), policy) {
				t.Fatal("changed current facts inherited prior exact consent")
			}
		})
	}
	for _, change := range []struct {
		name     string
		project  contract.ProjectID
		revision contract.PolicyRevisionID
		owner    contract.PrincipalID
	}{
		{"foreign policy", "foreign", "policy", "owner"},
		{"changed policy revision", "project", "changed-policy", "owner"},
		{"changed authorized owner", "project", "policy", "other-owner"},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed, err := contract.NewPolicy(change.project, change.revision, principalForTest(t, change.owner, contract.Human))
			if err != nil {
				t.Fatal(err)
			}
			if approved.HasApproval(proposal, changed) {
				t.Fatal("a changed governing policy retained approval eligibility")
			}
		})
	}
	if approved.HasApproval(contract.Proposal{}, policy) || approved.HasApproval(proposal, contract.Policy{}) || (contract.Consent{}).HasApproval(proposal, policy) {
		t.Fatal("absent inputs must not establish approval eligibility")
	}
}

func TestConsentRejectsSupersededApprovalWithoutRedirecting(t *testing.T) {
	state, proposal, policy, input := consentFixture(t)
	updated := consentBindingInput()
	updated.Reference.RevisionID = "r2"
	current, err := proposal.Revise(consentRevisionForTest(t, updated))
	if err != nil {
		t.Fatal(err)
	}
	next, result := applyConsentForTest(t, state, current, policy, input)
	if result.Outcome() != contract.ConsentRejected || result.Reason() != contract.ConsentReasonSupersededRevision || next.HasApproval(current, policy) {
		t.Fatal("a superseded reference was not rejected with its exact reason")
	}
	approved, _ := applyConsentForTest(t, state, proposal, policy, input)
	if approved.HasApproval(current, policy) {
		t.Fatal("superseding a revision transferred its historical approval")
	}
}

func subsequentConsentCommand(input contract.CommandInput, source string, action contract.ConsentAction, order contract.CommandOrder) contract.CommandInput {
	input.OperationID = contract.OperationID("operation-" + source)
	input.SourceCommandID = contract.SourceCommandID(source)
	input.Action = action
	input.Order = order
	return input
}

func TestConsentRevocationTombstoneFencesOlderCommands(t *testing.T) {
	for _, initiallyApproved := range []bool{false, true} {
		t.Run(map[bool]string{false: "without prior approval", true: "with prior approval"}[initiallyApproved], func(t *testing.T) {
			state, proposal, policy, input := consentFixture(t)
			if initiallyApproved {
				state, _ = applyConsentForTest(t, state, proposal, policy, input)
			}
			beforeRevoke := state
			revoke := subsequentConsentCommand(input, "revoke", contract.RevokeConsent, 20)
			revoked, result := applyConsentForTest(t, state, proposal, policy, revoke)
			want := contract.ConsentNoActiveApproval
			if initiallyApproved {
				want = contract.ConsentRevoked
			}
			if result.Outcome() != want || result.Reason() != contract.ConsentReasonNone || revoked.HasApproval(proposal, policy) {
				t.Fatalf("revocation outcome %d reason %d; want %d without active consent", result.Outcome(), result.Reason(), want)
			}
			if beforeRevoke.HasApproval(proposal, policy) != initiallyApproved {
				t.Fatal("revoking consent mutated an earlier state")
			}
			delayed := subsequentConsentCommand(input, "delayed-approve", contract.ApproveConsent, 15)
			stillRevoked, oldResult := applyConsentForTest(t, revoked, proposal, policy, delayed)
			if oldResult.Outcome() != contract.ConsentRejected || oldResult.Reason() != contract.ConsentReasonObsoleteCommand || stillRevoked.HasApproval(proposal, policy) {
				t.Fatal("older approval undid the revocation tombstone")
			}
			newer := subsequentConsentCommand(input, "explicit-reapprove", contract.ApproveConsent, 21)
			reapproved, newerResult := applyConsentForTest(t, stillRevoked, proposal, policy, newer)
			if newerResult.Outcome() != contract.ConsentApproved || !reapproved.HasApproval(proposal, policy) {
				t.Fatal("a new explicit eligible command did not restore consent")
			}
			for _, order := range []contract.CommandOrder{18, 21} {
				lateRevoke := subsequentConsentCommand(input, "late-revoke", contract.RevokeConsent, order)
				unchanged, staleResult := applyConsentForTest(t, reapproved, proposal, policy, lateRevoke)
				if staleResult.Outcome() != contract.ConsentRejected || staleResult.Reason() != contract.ConsentReasonObsoleteCommand || !unchanged.HasApproval(proposal, policy) {
					t.Fatal("an older or equal-order distinct revoke withdrew newer consent")
				}
			}
		})
	}
}

func TestConsentRevokesHistoricalOwnConsentWithoutWithdrawingCurrentRevision(t *testing.T) {
	empty, original, policy, input := consentFixture(t)
	approved, _ := applyConsentForTest(t, empty, original, policy, input)
	updated := consentBindingInput()
	updated.Reference.RevisionID = "r2"
	updated.PolicyRevision = "policy-2"
	current, err := original.Revise(consentRevisionForTest(t, updated))
	if err != nil {
		t.Fatal(err)
	}
	currentPolicy, err := contract.NewPolicy("project", "policy-2", input.Actor)
	if err != nil {
		t.Fatal(err)
	}
	newApproval := subsequentConsentCommand(input, "approve-r2", contract.ApproveConsent, 30)
	newApproval.Reference.RevisionID = "r2"
	currentApproved, _ := applyConsentForTest(t, approved, current, currentPolicy, newApproval)
	revoke := subsequentConsentCommand(input, "revoke-historical-r1", contract.RevokeConsent, 20)
	next, result := applyConsentForTest(t, currentApproved, current, currentPolicy, revoke)
	if result.Outcome() != contract.ConsentRevoked || result.Reason() != contract.ConsentReasonNone {
		t.Fatalf("historical revocation outcome %d reason %d; want own-consent withdrawal", result.Outcome(), result.Reason())
	}
	if next.HasApproval(original, policy) || !next.HasApproval(current, currentPolicy) {
		t.Fatal("historical revocation affected the wrong exact revision's consent")
	}
	if !currentApproved.HasApproval(original, policy) || !currentApproved.HasApproval(current, currentPolicy) {
		t.Fatal("historical revocation mutated its retained previous state")
	}
}

func TestConsentUnauthorizedRevocationCannotWithdrawOrPoisonOwnerOrdering(t *testing.T) {
	for _, actor := range []contract.Principal{
		principalForTest(t, "owner", contract.Agent),
		principalForTest(t, "owner", contract.Service),
		principalForTest(t, "other-human", contract.Human),
	} {
		empty, proposal, policy, input := consentFixture(t)
		approved, _ := applyConsentForTest(t, empty, proposal, policy, input)
		unauthorized := subsequentConsentCommand(input, "unauthorized-revoke", contract.RevokeConsent, 1000)
		unauthorized.Actor = actor
		unchanged, result := applyConsentForTest(t, approved, proposal, policy, unauthorized)
		if result.Outcome() != contract.ConsentRejected || result.Reason() != contract.ConsentReasonUnauthorized || !unchanged.HasApproval(proposal, policy) {
			t.Fatal("unauthorized revocation changed the owner's consent")
		}
		ownerRevoke := subsequentConsentCommand(input, "owner-revoke", contract.RevokeConsent, 20)
		revoked, ownerResult := applyConsentForTest(t, unchanged, proposal, policy, ownerRevoke)
		if ownerResult.Outcome() != contract.ConsentRevoked || revoked.HasApproval(proposal, policy) {
			t.Fatal("unauthorized ordering information prevented the owner's later withdrawal")
		}
	}
}
