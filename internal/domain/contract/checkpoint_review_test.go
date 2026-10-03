package contract

import (
	"errors"
	"testing"
)

func TestReviewAmbiguousCaseVariantVersion(t *testing.T) {
	if _, err := RestoreStateCheckpoint([]byte(`{"version":99,"Version":1}`)); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("unsupported version hidden by case-variant duplicate: %v", err)
	}
}

func TestReviewStandaloneApprovedReceiptCannotClaimAgentApproval(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{CommandResult: checkpointFixture(t).CommandResult})
	if err != nil {
		t.Fatal(err)
	}
	encoded = checkpointJSONChange(t, encoded, "result/Command/Actor/Kind", Agent)
	if _, err := RestoreStateCheckpoint(encoded); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("approved receipt accepted restricted agent actor: %v", err)
	}
}

func TestReviewConsentAcceptedOrderCannotReuseRejectedCommandOrder(t *testing.T) {
	state := checkpointFixture(t)
	owner := checkpointValue(NewPrincipal("new-owner", Human))
	consent := checkpointValue(NewConsent("project", "suite", "proposal"))
	ref := state.Proposal.Current().Binding().Reference()
	wrong := checkpointValue(NewCommand(CommandInput{OperationID: "wrong", SourceCommandID: "wrong-source", Actor: owner, Reference: ref, Carrier: "wrong-carrier", Action: ApproveConsent, Order: 1}))
	consent, rejected, err := consent.Apply(state.Proposal, state.Policy, wrong)
	if err != nil || rejected.Outcome() != ConsentRejected {
		t.Fatalf("valid rejected fixture: %v %v", rejected, err)
	}
	right := checkpointValue(NewCommand(CommandInput{OperationID: "right", SourceCommandID: "right-source", Actor: owner, Reference: ref, Carrier: state.Proposal.Current().Carrier(), Action: ApproveConsent, Order: 2}))
	consent, accepted, err := consent.Apply(state.Proposal, state.Policy, right)
	if err != nil || accepted.Outcome() != ConsentApproved {
		t.Fatalf("valid accepted fixture: %v %v", accepted, err)
	}
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Proposal: state.Proposal, Consent: consent})
	if err != nil {
		t.Fatal(err)
	}
	encoded = checkpointJSONChange(t, encoded, "consent/Results/1/Command/Order", 1)
	encoded = checkpointJSONChange(t, encoded, "consent/States/0/Order", 1)
	if _, err := RestoreStateCheckpoint(encoded); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("accepted consent reused rejected command order: %v", err)
	}
}

func TestReviewPriorityAcceptedOrderCannotReuseRejectedCommandOrder(t *testing.T) {
	state := checkpointFixture(t)
	owner := checkpointValue(NewPrincipal("new-owner", Human))
	schedule := checkpointValue(NewSchedule("project", "suite"))
	schedule = checkpointValue(schedule.Admit(state.Proposal, true))
	wrong := checkpointValue(NewPriorityCommand(PriorityCommandInput{OperationID: "wrong-priority", SourceCommandID: "wrong-priority-source", Actor: owner, ProjectID: "project", SuiteID: "suite", ProposalID: "unknown", Carrier: "unknown-carrier", Order: 1}))
	schedule, rejected, err := schedule.RequestPriority(state.Policy, wrong)
	if err != nil || rejected.Outcome() != PriorityRejected {
		t.Fatalf("valid rejected fixture: %v %v", rejected, err)
	}
	right := checkpointValue(NewPriorityCommand(PriorityCommandInput{OperationID: "right-priority", SourceCommandID: "right-priority-source", Actor: owner, ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", Carrier: state.Proposal.Current().Carrier(), Order: 2}))
	schedule, accepted, err := schedule.RequestPriority(state.Policy, right)
	if err != nil || accepted.Outcome() != PriorityAlreadyActive {
		t.Fatalf("valid accepted fixture: %v %v", accepted, err)
	}
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Scheduling: schedule})
	if err != nil {
		t.Fatal(err)
	}
	encoded = checkpointJSONChange(t, encoded, "scheduling/Results/1/Command/Order", 1)
	encoded = checkpointJSONChange(t, encoded, "scheduling/Order", 1)
	if _, err := RestoreStateCheckpoint(encoded); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("accepted priority reused rejected command order: %v", err)
	}
}
