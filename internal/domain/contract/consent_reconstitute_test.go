package contract_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// reconstitutionHistory is a Consent produced only by Apply over a proposal
// with a superseded and a current revision. Together with the next-command
// probes it pins that stored facts reconstitute into identical behavior.
type reconstitutionHistory struct {
	proposal contract.Proposal
	policy   contract.Policy
	owner    contract.Principal
	consent  contract.Consent
	aliases  map[contract.OperationID]contract.SourceCommandID
	outcomes []contract.CommandResult
}

func reconstitutionRevisionInput(revision contract.ProposalRevisionID) contract.BindingInput {
	input := consentBindingInput()
	input.Reference.RevisionID = revision
	return input
}

func newReconstitutionHistory(t *testing.T) reconstitutionHistory {
	t.Helper()
	empty, _, policy, base := consentFixture(t)
	first := consentProposalForTest(t, reconstitutionRevisionInput("r1"))
	proposal, err := first.Revise(consentRevisionForTest(t, reconstitutionRevisionInput("r2")))
	if err != nil {
		t.Fatal(err)
	}
	other := principalForTest(t, "other", contract.Human)
	otherPolicy, err := contract.NewPolicy("project", "policy-2", base.Actor)
	if err != nil {
		t.Fatal(err)
	}
	current := base.Reference
	current.RevisionID = "r2"
	superseded := base.Reference
	superseded.RevisionID = "r1"
	unknown := base.Reference
	unknown.RevisionID = "r9"
	h := reconstitutionHistory{proposal: proposal, policy: policy, owner: base.Actor, consent: empty, aliases: map[contract.OperationID]contract.SourceCommandID{}}
	apply := func(p contract.Policy, op, source string, actor contract.Principal, ref contract.ProposalReference, action contract.ConsentAction, order contract.CommandOrder, want contract.ConsentOutcome, reason contract.ConsentReason) {
		t.Helper()
		command := consentCommandForTest(t, contract.CommandInput{
			OperationID: contract.OperationID(op), SourceCommandID: contract.SourceCommandID(source), Actor: actor,
			Reference: ref, Carrier: "carrier", Action: action, Order: order,
		})
		next, result, err := h.consent.Apply(proposal, p, command)
		if err != nil || result.Outcome() != want || result.Reason() != reason || result.Duplicate() {
			t.Fatalf("%s: outcome=%v reason=%v duplicate=%v err=%v; want %v/%v", op, result.Outcome(), result.Reason(), result.Duplicate(), err, want, reason)
		}
		h.consent = next
		h.outcomes = append(h.outcomes, result)
	}
	approve, revoke := contract.ApproveConsent, contract.RevokeConsent
	apply(policy, "op1", "src1", h.owner, current, approve, 10, contract.ConsentApproved, contract.ConsentReasonNone)
	apply(policy, "op2", "src2", other, current, approve, 5, contract.ConsentRejected, contract.ConsentReasonUnauthorized)
	apply(otherPolicy, "op3", "src3", h.owner, current, approve, 11, contract.ConsentRejected, contract.ConsentReasonPolicyMismatch)
	apply(policy, "op4", "src4", h.owner, superseded, approve, 12, contract.ConsentRejected, contract.ConsentReasonSupersededRevision)
	apply(policy, "op5", "src5", h.owner, unknown, approve, 13, contract.ConsentRejected, contract.ConsentReasonUnknownRevision)
	apply(policy, "op6", "src6", h.owner, current, approve, 9, contract.ConsentRejected, contract.ConsentReasonObsoleteCommand)
	apply(policy, "op7", "src7", h.owner, current, revoke, 20, contract.ConsentRevoked, contract.ConsentReasonNone)
	apply(policy, "op8", "src8", h.owner, current, revoke, 21, contract.ConsentNoActiveApproval, contract.ConsentReasonNone)
	apply(policy, "op9", "src9", h.owner, current, approve, 30, contract.ConsentApproved, contract.ConsentReasonNone)
	apply(policy, "op10", "src10", h.owner, superseded, revoke, 40, contract.ConsentNoActiveApproval, contract.ConsentReasonNone)
	alias := consentCommandForTest(t, contract.CommandInput{
		OperationID: "alias-1", SourceCommandID: "src1", Actor: h.owner, Reference: current, Carrier: "carrier", Action: approve, Order: 10,
	})
	next, result, err := h.consent.Apply(proposal, policy, alias)
	if err != nil || !result.Duplicate() || result.Command().OperationID() != "op1" {
		t.Fatalf("alias did not replay the original result: %+v %v", result, err)
	}
	h.consent = next
	h.aliases["alias-1"] = "src1"
	return h
}

func (h reconstitutionHistory) rebuilt(t *testing.T) contract.Consent {
	t.Helper()
	rebuilt, err := contract.ReconstituteConsent(h.proposal, h.consent.Results(), h.aliases)
	if err != nil {
		t.Fatalf("stored history did not reconstitute: %v", err)
	}
	return rebuilt
}

func TestReconstituteConsentBehavesLikeTheOriginal(t *testing.T) {
	h := newReconstitutionHistory(t)
	rebuilt := h.rebuilt(t)
	if !reflect.DeepEqual(rebuilt.Results(), h.consent.Results()) || len(rebuilt.Results()) != len(h.outcomes) {
		t.Fatalf("results differ: got %d, want %d", len(rebuilt.Results()), len(h.outcomes))
	}
	if !h.consent.HasApproval(h.proposal, h.policy) || rebuilt.HasApproval(h.proposal, h.policy) != h.consent.HasApproval(h.proposal, h.policy) {
		t.Fatal("rebuilt consent changed approval eligibility")
	}
	base := consentBindingInput().Reference
	ref := func(revision contract.ProposalRevisionID) contract.ProposalReference {
		base.RevisionID = revision
		return base
	}
	other := principalForTest(t, "other", contract.Human)
	probes := map[string]contract.CommandInput{
		"duplicate source, new operation":      {OperationID: "new-op", SourceCommandID: "src1", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 10},
		"replay of an operation":               {OperationID: "op1", SourceCommandID: "src1", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 10},
		"replay of an alias":                   {OperationID: "alias-1", SourceCommandID: "src1", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 10},
		"alias operation, other source":        {OperationID: "alias-1", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 50},
		"operation, other source":              {OperationID: "op1", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 50},
		"source, other actor":                  {OperationID: "new-op", SourceCommandID: "src1", Actor: other, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 10},
		"later approval":                       {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 31},
		"approval at a used order":             {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 30},
		"approval at a rejected order":         {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 9},
		"later revocation":                     {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r2"), Action: contract.RevokeConsent, Order: 50},
		"revocation of the historical entry":   {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r1"), Action: contract.RevokeConsent, Order: 41},
		"revocation below the historical mark": {OperationID: "new-op", SourceCommandID: "fresh", Actor: h.owner, Reference: ref("r1"), Action: contract.RevokeConsent, Order: 39},
		"unauthorized actor":                   {OperationID: "new-op", SourceCommandID: "fresh", Actor: other, Reference: ref("r2"), Action: contract.ApproveConsent, Order: 60},
	}
	for name, input := range probes {
		t.Run(name, func(t *testing.T) {
			input.Carrier = "carrier"
			command := consentCommandForTest(t, input)
			wantState, want, wantErr := h.consent.Apply(h.proposal, h.policy, command)
			gotState, got, gotErr := rebuilt.Apply(h.proposal, h.policy, command)
			if wantErr != nil || gotErr != nil || got != want {
				t.Fatalf("next Apply differs: got %+v (%v), want %+v (%v)", got, gotErr, want, wantErr)
			}
			if !reflect.DeepEqual(gotState.Results(), wantState.Results()) || gotState.HasApproval(h.proposal, h.policy) != wantState.HasApproval(h.proposal, h.policy) {
				t.Fatal("state after the next Apply differs")
			}
			if again, err := contract.ReconstituteConsent(h.proposal, gotState.Results(), h.aliases); err != nil || !reflect.DeepEqual(again.Results(), gotState.Results()) {
				t.Fatalf("history extended after reconstitution does not reconstitute: %v", err)
			}
		})
	}
}

func TestReconstituteConsentOfAnEmptyHistory(t *testing.T) {
	_, proposal, policy, input := consentFixture(t)
	rebuilt, err := contract.ReconstituteConsent(proposal, nil, nil)
	if err != nil || len(rebuilt.Results()) != 0 || rebuilt.HasApproval(proposal, policy) {
		t.Fatalf("empty history: %v", err)
	}
	if _, result := applyConsentForTest(t, rebuilt, proposal, policy, input); result.Outcome() != contract.ConsentApproved {
		t.Fatal("reconstituted empty consent did not accept the first command")
	}
}

func TestReconstituteCommandResultMatchesApply(t *testing.T) {
	h := newReconstitutionHistory(t)
	for _, original := range h.outcomes {
		got, err := contract.ReconstituteCommandResult(original.Command(), original.Outcome(), original.Reason())
		if err != nil || got != original || got.Duplicate() {
			t.Fatalf("result of %s did not reconstitute: %+v %v", original.Command().OperationID(), got, err)
		}
	}
}

func TestReconstituteCommandResultRejectsInconsistentFacts(t *testing.T) {
	_, _, _, input := consentFixture(t)
	command := consentCommandForTest(t, input)
	revokeInput := input
	revokeInput.Action = contract.RevokeConsent
	revoke := consentCommandForTest(t, revokeInput)
	for name, tc := range map[string]struct {
		command contract.Command
		outcome contract.ConsentOutcome
		reason  contract.ConsentReason
	}{
		"approve recorded as revoked":     {command, contract.ConsentRevoked, contract.ConsentReasonNone},
		"approve recorded as no approval": {command, contract.ConsentNoActiveApproval, contract.ConsentReasonNone},
		"revoke recorded as approved":     {revoke, contract.ConsentApproved, contract.ConsentReasonNone},
		"zero command":                    {contract.Command{}, contract.ConsentApproved, contract.ConsentReasonNone},
		"absent outcome":                  {command, 0, contract.ConsentReasonNone},
		"unknown outcome":                 {command, 99, contract.ConsentReasonNone},
		"unknown reason":                  {command, contract.ConsentRejected, 99},
		"accepted with a reason":          {command, contract.ConsentApproved, contract.ConsentReasonUnauthorized},
		"rejected without reason":         {command, contract.ConsentRejected, contract.ConsentReasonNone},
		"rejected as a conflict":          {command, contract.ConsentRejected, contract.ConsentReasonCommandConflict},
		"revoked with a reason":           {command, contract.ConsentRevoked, contract.ConsentReasonObsoleteCommand},
		"no approval with a reason":       {command, contract.ConsentNoActiveApproval, contract.ConsentReasonUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := contract.ReconstituteCommandResult(tc.command, tc.outcome, tc.reason)
			if !errors.Is(err, contract.ErrInvalidCommand) || got != (contract.CommandResult{}) {
				t.Fatalf("inconsistent result accepted: %+v %v", got, err)
			}
		})
	}
}

func TestReconstituteConsentRejectsInconsistentInput(t *testing.T) {
	h := newReconstitutionHistory(t)
	stored := h.consent.Results()
	foreignProposal := consentProposalForTest(t, func() contract.BindingInput {
		input := consentBindingInput()
		input.Reference.ProposalID = "other-proposal"
		return input
	}())
	foreignConsent, err := contract.NewConsent("project", "suite", "other-proposal")
	if err != nil {
		t.Fatal(err)
	}
	foreignInput := contract.CommandInput{OperationID: "foreign-op", SourceCommandID: "foreign-src", Actor: h.owner, Reference: foreignProposal.Current().Binding().Reference(), Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}
	_, foreignResult := applyConsentForTest(t, foreignConsent, foreignProposal, h.policy, foreignInput)

	_, duplicate, err := h.consent.Apply(h.proposal, h.policy, stored[0].Command())
	if err != nil || !duplicate.Duplicate() {
		t.Fatalf("setup: expected duplicate result, got %+v %v", duplicate, err)
	}
	conflicting := consentCommandForTest(t, contract.CommandInput{OperationID: "op1", SourceCommandID: "other-source", Actor: h.owner, Reference: stored[0].Command().Reference(), Carrier: "carrier", Action: contract.ApproveConsent, Order: 77})
	_, conflict, err := h.consent.Apply(h.proposal, h.policy, conflicting)
	if err != nil || conflict.Reason() != contract.ConsentReasonCommandConflict {
		t.Fatalf("setup: expected conflict result, got %+v %v", conflict, err)
	}

	current := consentBindingInput().Reference
	current.RevisionID = "r2"
	reconstituted := func(op, source string, action contract.ConsentAction, order contract.CommandOrder, revision contract.ProposalRevisionID, outcome contract.ConsentOutcome) contract.CommandResult {
		t.Helper()
		ref := current
		ref.RevisionID = revision
		command := consentCommandForTest(t, contract.CommandInput{OperationID: contract.OperationID(op), SourceCommandID: contract.SourceCommandID(source), Actor: h.owner, Reference: ref, Carrier: "carrier", Action: action, Order: order})
		result, err := contract.ReconstituteCommandResult(command, outcome, contract.ConsentReasonNone)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	approve, revoke := contract.ApproveConsent, contract.RevokeConsent
	for name, tc := range map[string]struct {
		results []contract.CommandResult
		aliases map[contract.OperationID]contract.SourceCommandID
	}{
		"result of another aggregate":         {[]contract.CommandResult{foreignResult}, nil},
		"duplicate-flagged result":            {[]contract.CommandResult{duplicate}, nil},
		"command-conflict result":             {[]contract.CommandResult{conflict}, nil},
		"zero result":                         {[]contract.CommandResult{{}}, nil},
		"repeated order":                      {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r2", contract.ConsentApproved), reconstituted("b", "sb", approve, 5, "r2", contract.ConsentApproved)}, nil},
		"decreasing order":                    {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r2", contract.ConsentApproved), reconstituted("b", "sb", revoke, 4, "r2", contract.ConsentRevoked)}, nil},
		"unknown revision":                    {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r9", contract.ConsentApproved)}, nil},
		"repeated source command":             {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r2", contract.ConsentApproved), reconstituted("b", "sa", revoke, 6, "r2", contract.ConsentRevoked)}, nil},
		"repeated operation":                  {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r2", contract.ConsentApproved), reconstituted("a", "sb", revoke, 6, "r2", contract.ConsentRevoked)}, nil},
		"outcome contradicts action":          {[]contract.CommandResult{reconstituted("a", "sa", revoke, 5, "r2", contract.ConsentApproved)}, nil},
		"revoked without active approval":     {[]contract.CommandResult{reconstituted("a", "sa", revoke, 5, "r2", contract.ConsentRevoked)}, nil},
		"no-active over an active approval":   {[]contract.CommandResult{reconstituted("a", "sa", approve, 5, "r2", contract.ConsentApproved), reconstituted("b", "sb", revoke, 6, "r2", contract.ConsentNoActiveApproval)}, nil},
		"alias for an unknown source command": {stored, map[contract.OperationID]contract.SourceCommandID{"alias": "unknown-source"}},
		"alias reuses an operation":           {stored, map[contract.OperationID]contract.SourceCommandID{"op1": "src2"}},
		"blank alias operation":               {stored, map[contract.OperationID]contract.SourceCommandID{"": "src1"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := contract.ReconstituteConsent(h.proposal, tc.results, tc.aliases)
			if !errors.Is(err, contract.ErrInvalidConsent) || len(got.Results()) != 0 {
				t.Fatalf("inconsistent input accepted: %d results, %v", len(got.Results()), err)
			}
		})
	}
	if _, err := contract.ReconstituteConsent(contract.Proposal{}, stored, nil); !errors.Is(err, contract.ErrInvalidConsent) {
		t.Fatalf("zero proposal accepted: %v", err)
	}
}
