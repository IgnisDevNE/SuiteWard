package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// applyFaults is a command with the context it is applied in, so a row can break two things at once.
type applyFaults struct {
	state    contract.Consent
	proposal contract.Proposal
	policy   contract.Policy
	input    contract.CommandInput
}

func (f *applyFaults) apply(t *testing.T, input contract.CommandInput) {
	t.Helper()
	f.state, _ = applyConsentForTest(t, f.state, f.proposal, f.policy, input)
}

func (f *applyFaults) changePolicy(t *testing.T) {
	t.Helper()
	var err error
	if f.policy, err = contract.NewPolicy("project", "new-policy", principalForTest(t, "owner", contract.Human)); err != nil {
		t.Fatal(err)
	}
}

func (f *applyFaults) supersedeRevision(t *testing.T) {
	t.Helper()
	updated := consentBindingInput()
	updated.Reference.RevisionID = "r2"
	var err error
	if f.proposal, err = f.proposal.Revise(consentRevisionForTest(t, updated)); err != nil {
		t.Fatal(err)
	}
}

// TestConsentApplyDecidesSimultaneousFaultsInOrder gives each command two faults at once and pins the one the result
// names, so the order of Apply's checks cannot change unnoticed: replay before eligibility, a missing or superseded
// revision before authorization, authorization before the policy, the policy before the order of earlier commands.
func TestConsentApplyDecidesSimultaneousFaultsInOrder(t *testing.T) {
	type want struct {
		outcome   contract.ConsentOutcome
		reason    contract.ConsentReason
		duplicate bool
	}
	rejected := func(reason contract.ConsentReason) want {
		return want{outcome: contract.ConsentRejected, reason: reason}
	}
	for _, tt := range []struct {
		name    string
		prepare func(*testing.T, *applyFaults)
		want    want
	}{
		{"unknown revision before an unauthorized actor", func(t *testing.T, f *applyFaults) {
			f.input.Reference.RevisionID = "unknown"
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
		}, rejected(contract.ConsentReasonUnknownRevision)},
		{"superseded revision before an unauthorized actor", func(t *testing.T, f *applyFaults) {
			f.supersedeRevision(t)
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
		}, rejected(contract.ConsentReasonSupersededRevision)},
		{"another carrier before an unauthorized actor", func(t *testing.T, f *applyFaults) {
			f.input.Carrier = "another-carrier"
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
		}, rejected(contract.ConsentReasonContextMismatch)},
		{"unknown revision before a changed policy", func(t *testing.T, f *applyFaults) {
			f.input.Reference.RevisionID = "unknown"
			f.changePolicy(t)
		}, rejected(contract.ConsentReasonUnknownRevision)},
		{"unknown revision of a revocation before an unauthorized actor", func(t *testing.T, f *applyFaults) {
			f.input.Action = contract.RevokeConsent
			f.input.Reference.RevisionID = "unknown"
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
		}, rejected(contract.ConsentReasonUnknownRevision)},
		{"unauthorized actor before a changed policy", func(t *testing.T, f *applyFaults) {
			f.input.Actor = principalForTest(t, "repo-admin", contract.Human)
			f.changePolicy(t)
		}, rejected(contract.ConsentReasonUnauthorized)},
		{"changed policy before an obsolete order", func(t *testing.T, f *applyFaults) {
			f.apply(t, f.input)
			f.changePolicy(t)
			f.input = subsequentConsentCommand(f.input, "older", contract.ApproveConsent, 5)
		}, rejected(contract.ConsentReasonPolicyMismatch)},
		{"unauthorized approval before the order of its own earlier rejection", func(t *testing.T, f *applyFaults) {
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
			f.input.Order = 100
			f.apply(t, f.input)
			f.input = subsequentConsentCommand(f.input, "second", contract.ApproveConsent, 100)
		}, rejected(contract.ConsentReasonUnauthorized)},
		{"unauthorized revocation before the order of its own earlier rejection", func(t *testing.T, f *applyFaults) {
			f.input.Actor = principalForTest(t, "owner", contract.Agent)
			f.input.Action = contract.RevokeConsent
			f.input.Order = 100
			f.apply(t, f.input)
			f.input = subsequentConsentCommand(f.input, "second", contract.RevokeConsent, 100)
		}, rejected(contract.ConsentReasonUnauthorized)},
		{"a stored rejection blocks the same order of a later eligible command", func(t *testing.T, f *applyFaults) {
			earlier := f.input
			earlier.Carrier = "another-carrier"
			earlier.Order = 100
			f.apply(t, earlier)
			f.input = subsequentConsentCommand(f.input, "later", contract.ApproveConsent, 100)
		}, rejected(contract.ConsentReasonObsoleteCommand)},
		{"an unknown revision before the order its own stored rejection holds", func(t *testing.T, f *applyFaults) {
			f.input.Reference.RevisionID = "unknown"
			f.input.Order = 100
			f.apply(t, f.input)
			f.input = subsequentConsentCommand(f.input, "second", contract.ApproveConsent, 100)
		}, rejected(contract.ConsentReasonUnknownRevision)},
		{"a superseded revision before the order its own stored rejection holds", func(t *testing.T, f *applyFaults) {
			f.supersedeRevision(t)
			f.input.Order = 100
			f.apply(t, f.input)
			f.input = subsequentConsentCommand(f.input, "second", contract.ApproveConsent, 100)
		}, rejected(contract.ConsentReasonSupersededRevision)},
		{"replay before an unknown revision, a changed policy and a new order", func(t *testing.T, f *applyFaults) {
			f.apply(t, f.input)
			f.input.Reference.RevisionID = "unknown"
			f.input.Order = 1000
			f.changePolicy(t)
		}, want{outcome: contract.ConsentApproved, duplicate: true}},
		{"replay of a rejection before the eligibility it lacked", func(t *testing.T, f *applyFaults) {
			f.input.Reference.RevisionID = "r2"
			f.apply(t, f.input) // unknown at the time
			f.supersedeRevision(t)
		}, want{outcome: contract.ConsentRejected, reason: contract.ConsentReasonUnknownRevision, duplicate: true}},
		{"an operation of another source before a replay of the source", func(t *testing.T, f *applyFaults) {
			f.apply(t, f.input)
			second := subsequentConsentCommand(f.input, "second", contract.ApproveConsent, 11)
			f.apply(t, second)
			f.input.SourceCommandID = second.SourceCommandID // the first operation, the second source
		}, rejected(contract.ConsentReasonCommandConflict)},
		{"a source of another actor before the eligibility of the command", func(t *testing.T, f *applyFaults) {
			f.apply(t, f.input)
			f.input.Actor = principalForTest(t, "repo-admin", contract.Human)
			f.input.OperationID = "other-operation"
			f.input.Reference.RevisionID = "unknown"
		}, rejected(contract.ConsentReasonCommandConflict)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state, proposal, policy, input := consentFixture(t)
			faults := applyFaults{state: state, proposal: proposal, policy: policy, input: input}
			tt.prepare(t, &faults)
			before := len(faults.state.Results())
			next, result, err := faults.state.Apply(faults.proposal, faults.policy, consentCommandForTest(t, faults.input))
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome() != tt.want.outcome || result.Reason() != tt.want.reason || result.Duplicate() != tt.want.duplicate {
				t.Fatalf("outcome %d reason %d duplicate %v; want %d, %d, %v", result.Outcome(), result.Reason(), result.Duplicate(), tt.want.outcome, tt.want.reason, tt.want.duplicate)
			}
			wantNew := 1 // a replay and a conflict record nothing
			if tt.want.duplicate || tt.want.reason == contract.ConsentReasonCommandConflict {
				wantNew = 0
			}
			if got := len(next.Results()) - before; got != wantNew {
				t.Fatalf("recorded %d results; want %d", got, wantNew)
			}
		})
	}
	t.Run("an absent command before an absent aggregate", func(t *testing.T) {
		_, proposal, policy, _ := consentFixture(t)
		if _, _, err := (contract.Consent{}).Apply(proposal, policy, contract.Command{}); !errors.Is(err, contract.ErrInvalidCommand) {
			t.Fatalf("error %v; want ErrInvalidCommand", err)
		}
	})
}

// A rejection stored at an order blocks that order for later results of the same actor and revision, as Apply does, so
// a history in which an approval repeats it was not produced by Apply.
func TestReconstituteConsentRefusesAnOrderRepeatedAfterARejection(t *testing.T) {
	h := newReconstitutionHistory(t)
	current := consentBindingInput().Reference
	current.RevisionID = "r2"
	result := func(op, source string, outcome contract.ConsentOutcome, reason contract.ConsentReason) contract.CommandResult {
		t.Helper()
		command := consentCommandForTest(t, contract.CommandInput{OperationID: contract.OperationID(op), SourceCommandID: contract.SourceCommandID(source), Actor: h.owner,
			Reference: current, Carrier: "carrier", Action: contract.ApproveConsent, Order: 9})
		stored, err := contract.ReconstituteCommandResult(command, outcome, reason)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	rejected := result("a", "sa", contract.ConsentRejected, contract.ConsentReasonObsoleteCommand)
	approved := result("b", "sb", contract.ConsentApproved, contract.ConsentReasonNone)
	got, err := contract.ReconstituteConsent(h.proposal, []contract.CommandResult{rejected, approved}, nil)
	if !errors.Is(err, contract.ErrInvalidConsent) || len(got.Results()) != 0 {
		t.Fatalf("an approval at the order of an earlier rejection was accepted: %d results, %v", len(got.Results()), err)
	}
	if _, err := contract.ReconstituteConsent(h.proposal, []contract.CommandResult{rejected}, nil); err != nil {
		t.Fatalf("the rejection alone does not reconstitute: %v", err)
	}
}
