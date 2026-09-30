package contract

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func schedulingProposal(t *testing.T, project ProjectID, suite SuiteID, id ProposalID, carrier ApprovalCarrierID, revision ProposalRevisionID) Proposal {
	t.Helper()
	binding, err := NewApprovalBinding(BindingInput{
		Reference: ProposalReference{ProjectID: project, SuiteID: suite, ProposalID: id, RevisionID: revision},
		Manifest:  artifact.Hash([]byte("manifest")), Scope: artifact.Hash([]byte("scope")), PolicyRevision: "policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := NewProposalRevision(binding, "source", carrier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewProposal(rev)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func TestScheduleRejectsInvalidAdmission(t *testing.T) {
	for _, ids := range [][2]string{{"", "suite"}, {" \t", "suite"}, {"project", ""}, {"project", "\n"}} {
		if s, err := NewSchedule(ProjectID(ids[0]), SuiteID(ids[1])); !errors.Is(err, ErrInvalidSchedule) || !s.IsZero() {
			t.Errorf("invalid IDs accepted: %q => %+v, %v", ids, s, err)
		}
	}
	s, a, _ := schedulingPair(t)
	for _, tc := range []struct {
		name     string
		schedule Schedule
		proposal Proposal
		want     error
	}{
		{"zero schedule", Schedule{}, a, ErrInvalidSchedule},
		{"zero proposal", s, Proposal{}, ErrInvalidSchedule},
		{"foreign project", s, schedulingProposal(t, "other", "suite", "a", "carrier-a", "r1"), ErrScheduleContextMismatch},
		{"foreign suite", s, schedulingProposal(t, "project", "other", "a", "carrier-a", "r1"), ErrScheduleContextMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, changing := range []bool{false, true} {
				next, err := tc.schedule.Admit(tc.proposal, changing)
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(next, tc.schedule) || tc.schedule.CanPromote(tc.proposal, tc.schedule.Generation()) {
					t.Fatalf("invalid admission changed schedule or became eligible: %v", err)
				}
			}
		})
	}
	s, err := s.Admit(a, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Proposal{
		schedulingProposal(t, "project", "suite", "a", "other-carrier", "r1"),
		schedulingProposal(t, "project", "suite", "other", "carrier-a", "r1"),
	} {
		next, err := s.Admit(p, true)
		if !errors.Is(err, ErrScheduleConflict) || !reflect.DeepEqual(next, s) || s.CanPromote(p, s.Generation()) {
			t.Errorf("identity collision not rejected unchanged: %v", err)
		}
	}
	kept, err := s.Admit(a, false)
	if err != nil || !kept.CanPromote(a, kept.Generation()) {
		t.Fatal("false classification withdrew existing active entry")
	}
	full, a, _ := schedulingPair(t)
	full.generation = ScheduleGeneration(math.MaxUint64)
	next, err := full.Admit(a, true)
	if !errors.Is(err, ErrScheduleGenerationExhausted) || !reflect.DeepEqual(next, full) {
		t.Fatal("generation overflow changed state")
	}
	spaced, err := NewSchedule(" project ", " suite ")
	if err != nil || spaced.ProjectID() != " project " || spaced.SuiteID() != " suite " {
		t.Fatal("accepted ID bytes were normalized")
	}
}

func schedulingPair(t *testing.T) (Schedule, Proposal, Proposal) {
	t.Helper()
	s, err := NewSchedule("project", "suite")
	if err != nil {
		t.Fatal(err)
	}
	a := schedulingProposal(t, "project", "suite", "a", "carrier-a", "r1")
	b := schedulingProposal(t, "project", "suite", "b", "carrier-b", "r1")
	return s, a, b
}

func schedulingPolicy(t *testing.T) (Policy, Principal) {
	t.Helper()
	owner, err := NewPrincipal("owner", Human)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewPolicy("project", "policy", owner)
	if err != nil {
		t.Fatal(err)
	}
	return policy, owner
}

func schedulingCommand(t *testing.T, actor Principal, id ProposalID, carrier ApprovalCarrierID, order CommandOrder) PriorityCommand {
	t.Helper()
	command, err := NewPriorityCommand(PriorityCommandInput{OperationID: OperationID(id), SourceCommandID: SourceCommandID(id), Actor: actor, ProjectID: "project", SuiteID: "suite", ProposalID: id, Carrier: carrier, Order: order})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func TestSchedulePriorityCommand(t *testing.T) {
	_, owner := schedulingPolicy(t)
	input := PriorityCommandInput{OperationID: "op", SourceCommandID: "source", Actor: owner, ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", Carrier: "carrier", Order: 3}
	c, err := NewPriorityCommand(input)
	if err != nil || c.OperationID() != input.OperationID || c.SourceCommandID() != input.SourceCommandID || c.Actor() != owner || c.ProjectID() != input.ProjectID || c.SuiteID() != input.SuiteID || c.ProposalID() != input.ProposalID || c.Carrier() != input.Carrier || c.Order() != 3 {
		t.Fatal("priority command lost source, target or actor identity")
	}
	for name, mutate := range map[string]func(*PriorityCommandInput){
		"operation": func(i *PriorityCommandInput) { i.OperationID = " \t" }, "source": func(i *PriorityCommandInput) { i.SourceCommandID = "" },
		"actor": func(i *PriorityCommandInput) { i.Actor = Principal{} }, "project": func(i *PriorityCommandInput) { i.ProjectID = "" },
		"suite": func(i *PriorityCommandInput) { i.SuiteID = "\n" }, "proposal": func(i *PriorityCommandInput) { i.ProposalID = "" },
		"carrier": func(i *PriorityCommandInput) { i.Carrier = "" }, "order": func(i *PriorityCommandInput) { i.Order = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := input
			mutate(&bad)
			if c, err := NewPriorityCommand(bad); !errors.Is(err, ErrInvalidPriorityCommand) || c != (PriorityCommand{}) {
				t.Fatalf("invalid command accepted: %v", err)
			}
		})
	}
}

func TestSchedulePriorityRequests(t *testing.T) {
	s, a, b := schedulingPair(t)
	s = schedulingAdmit(t, schedulingAdmit(t, s, a), b)
	policy, owner := schedulingPolicy(t)
	activeCommand := schedulingCommand(t, owner, "a", "carrier-a", 1)
	noop, result, err := s.RequestPriority(policy, activeCommand)
	if err != nil || result.Outcome() != PriorityAlreadyActive || result.Reason() != PriorityReasonNone || result.Duplicate() || result.Command() != activeCommand || noop.Generation() != s.Generation() || !noop.CanPromote(a, s.Generation()) {
		t.Fatal("already-active request did not report a truthful unchanged-authority no-op")
	}
	command := schedulingCommand(t, owner, "b", "carrier-b", 2)
	pending, result, err := noop.RequestPriority(policy, command)
	if err != nil || result.Outcome() != PriorityRequested || result.Reason() != PriorityReasonNone || result.Command() != command || pending.Generation() == noop.Generation() {
		t.Fatal("authorized waiting request did not establish pending fence")
	}
	if got, ok := pending.PendingTransfer(); !ok || got != command {
		t.Fatal("pending request identity not retained")
	}
	if pending.CanPromote(a, pending.Generation()) || pending.CanPromote(b, pending.Generation()) || pending.CanPromote(a, s.Generation()) {
		t.Fatal("pending transfer authorized promotion")
	}
	if active, ok := pending.Active(); !ok || active.ProposalID() != "a" {
		t.Fatal("request itself activated replacement")
	}
	if _, ok := s.PendingTransfer(); ok || len(s.Results()) != 0 || len(noop.Results()) != 1 || len(pending.Results()) != 2 {
		t.Fatal("request mutated earlier schedule history")
	}
	receipts := pending.Results()
	receipts[0] = PriorityResult{}
	if pending.Results()[0].Outcome() != PriorityAlreadyActive {
		t.Fatal("receipt getter exposes backing slice")
	}
	newInput := activeCommand.input
	newInput.OperationID = "again"
	newInput.SourceCommandID = "again"
	newInput.Order = 3
	newCommand, err := NewPriorityCommand(newInput)
	if err != nil {
		t.Fatal(err)
	}
	rejected, result, err := pending.RequestPriority(policy, newCommand)
	if err != nil || result.Outcome() != PriorityRejected || result.Reason() != PriorityReasonTransferPending || rejected.Generation() != pending.Generation() {
		t.Fatal("new command bypassed pending state with already-active no-op")
	}
	for _, event := range []ScheduleObservation{ObserveIntegrated, ObserveClosedUnmerged, ObservePromoted} {
		next, err := pending.Observe(a, pending.Generation(), event)
		if !errors.Is(err, ErrScheduleConflict) || !reflect.DeepEqual(next, pending) {
			t.Fatal("ordinary active observation bypassed transfer reconciliation")
		}
	}
	s.generation = ScheduleGeneration(math.MaxUint64)
	next, _, err := s.RequestPriority(policy, command)
	if !errors.Is(err, ErrScheduleGenerationExhausted) || !reflect.DeepEqual(next, s) {
		t.Fatal("request overflow mutated entries or receipts")
	}
}

func TestSchedulePriorityRejections(t *testing.T) {
	s, a, b := schedulingPair(t)
	s = schedulingAdmit(t, schedulingAdmit(t, s, a), b)
	policy, owner := schedulingPolicy(t)
	for _, kind := range []PrincipalKind{Human, Agent, Service} {
		actor, err := NewPrincipal("intruder", kind)
		if err != nil {
			t.Fatal(err)
		}
		command := schedulingCommand(t, actor, "b", "carrier-b", 2)
		next, result, err := s.RequestPriority(policy, command)
		if err != nil || result.Outcome() != PriorityRejected || result.Reason() != PriorityReasonUnauthorized || next.Generation() != s.Generation() || len(next.Results()) != 1 {
			t.Fatal("unauthorized request not rejected and recorded without authority change")
		}
	}
	for _, tc := range []struct {
		name     string
		schedule Schedule
		proposal ProposalID
		carrier  ApprovalCarrierID
		reason   PriorityReason
	}{
		{"unknown", s, "unknown", "unknown", PriorityReasonUnknownTarget},
		{"wrong carrier", s, "b", "wrong", PriorityReasonUnknownTarget},
		{"closed", schedulingObserve(t, s, b, ObserveClosedUnmerged), "b", "carrier-b", PriorityReasonClosedTarget},
		{"integrated", schedulingObserve(t, s, a, ObserveIntegrated), "a", "carrier-a", PriorityReasonMergedTarget},
		{"promoted", schedulingObserve(t, s, a, ObservePromoted), "a", "carrier-a", PriorityReasonMergedTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, result, err := tc.schedule.RequestPriority(policy, schedulingCommand(t, owner, tc.proposal, tc.carrier, 2))
			if err != nil || result.Outcome() != PriorityRejected || result.Reason() != tc.reason || next.Generation() != tc.schedule.Generation() {
				t.Fatalf("target rejection wrong: %+v, %v", result, err)
			}
		})
	}
	command := schedulingCommand(t, owner, "b", "carrier-b", 2)
	foreignPolicy, err := NewPolicy("other", "policy", owner)
	if err != nil {
		t.Fatal(err)
	}
	foreignProject := command.input
	foreignProject.ProjectID = "other"
	foreignSuite := command.input
	foreignSuite.SuiteID = "other"
	projectCommand, _ := NewPriorityCommand(foreignProject)
	suiteCommand, _ := NewPriorityCommand(foreignSuite)
	for _, tc := range []struct {
		s       Schedule
		policy  Policy
		command PriorityCommand
		want    error
	}{
		{Schedule{}, policy, command, ErrInvalidSchedule}, {s, policy, PriorityCommand{}, ErrInvalidPriorityCommand},
		{s, Policy{}, command, ErrScheduleContextMismatch}, {s, foreignPolicy, command, ErrScheduleContextMismatch},
		{s, policy, projectCommand, ErrScheduleContextMismatch}, {s, policy, suiteCommand, ErrScheduleContextMismatch},
	} {
		next, _, err := tc.s.RequestPriority(tc.policy, tc.command)
		if !errors.Is(err, tc.want) || !reflect.DeepEqual(next, tc.s) {
			t.Fatalf("invalid command context changed schedule: %v", err)
		}
	}
}

func schedulingAdmit(t *testing.T, s Schedule, p Proposal) Schedule {
	t.Helper()
	next, err := s.Admit(p, true)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func schedulingObserve(t *testing.T, s Schedule, p Proposal, event ScheduleObservation) Schedule {
	t.Helper()
	next, err := s.Observe(p, s.Generation(), event)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestScheduleObservations(t *testing.T) {
	s, a, b := schedulingPair(t)
	c := schedulingProposal(t, "project", "suite", "c", "carrier-c", "r1")
	s = schedulingAdmit(t, schedulingAdmit(t, schedulingAdmit(t, s, a), b), c)
	integrated := schedulingObserve(t, s, a, ObserveIntegrated)
	active, ok := integrated.Active()
	if !ok || active.ProposalID() != "a" || active.State() != ScheduleIntegratedPending || !integrated.CanPromote(a, integrated.Generation()) || integrated.CanPromote(a, s.Generation()) || integrated.CanPromote(b, integrated.Generation()) {
		t.Fatal("integration did not retain active pending ownership with a new fence")
	}
	if again := schedulingObserve(t, integrated, a, ObserveIntegrated); !reflect.DeepEqual(again, integrated) {
		t.Fatal("repeated integration was not a no-op")
	}
	promoted := schedulingObserve(t, integrated, a, ObservePromoted)
	if !promoted.CanPromote(b, promoted.Generation()) || promoted.CanPromote(a, promoted.Generation()) || promoted.CanPromote(b, integrated.Generation()) || promoted.Entries()[0].State() != SchedulePromoted {
		t.Fatal("committed promotion did not advance to earliest waiting entry")
	}
	if again := schedulingObserve(t, promoted, a, ObservePromoted); !reflect.DeepEqual(again, promoted) {
		t.Fatal("repeated promotion changed queue")
	}
	closed := schedulingObserve(t, promoted, b, ObserveClosedUnmerged)
	if !closed.CanPromote(c, closed.Generation()) || closed.Entries()[1].State() != ScheduleClosed {
		t.Fatal("closed-unmerged active did not advance queue")
	}
	if again := schedulingObserve(t, closed, b, ObserveClosedUnmerged); !reflect.DeepEqual(again, closed) {
		t.Fatal("repeated closure changed queue")
	}
	empty := schedulingObserve(t, closed, c, ObserveClosedUnmerged)
	if _, ok := empty.Active(); ok {
		t.Fatal("closed queue retained an active entry")
	}
	if kept := schedulingAdmit(t, empty, b); !reflect.DeepEqual(kept, empty) {
		t.Fatal("admission silently reopened closed proposal")
	}
	d := schedulingProposal(t, "project", "suite", "d", "carrier-d", "r1")
	if reopened := schedulingAdmit(t, empty, d); !reopened.CanPromote(d, reopened.Generation()) {
		t.Fatal("new proposal did not take free position")
	}
	waitingClosed := schedulingObserve(t, s, b, ObserveClosedUnmerged)
	if waitingClosed.Generation() != s.Generation() || !waitingClosed.CanPromote(a, s.Generation()) {
		t.Fatal("waiting closure invalidated active authority")
	}
	after := schedulingObserve(t, waitingClosed, a, ObservePromoted)
	if !after.CanPromote(c, after.Generation()) {
		t.Fatal("promotion failed to skip closed waiting entry")
	}
	if s.Entries()[0].State() != ScheduleActive || s.Entries()[1].State() != ScheduleWaiting {
		t.Fatal("observation mutated earlier snapshot")
	}
}

func TestScheduleObservationsRejectInvalidTransitions(t *testing.T) {
	s, a, b := schedulingPair(t)
	s = schedulingAdmit(t, schedulingAdmit(t, s, a), b)
	unknown := schedulingProposal(t, "project", "suite", "unknown", "carrier-unknown", "r1")
	wrongCarrier := schedulingProposal(t, "project", "suite", "a", "other-carrier", "r1")
	foreign := schedulingProposal(t, "other", "suite", "a", "carrier-a", "r1")
	integrated := schedulingObserve(t, s, a, ObserveIntegrated)
	closed := schedulingObserve(t, s, b, ObserveClosedUnmerged)
	promoted := schedulingObserve(t, s, a, ObservePromoted)
	for _, tc := range []struct {
		name       string
		s          Schedule
		p          Proposal
		generation ScheduleGeneration
		event      ScheduleObservation
		want       error
	}{
		{"zero schedule", Schedule{}, a, 0, ObserveIntegrated, ErrInvalidSchedule},
		{"zero proposal", s, Proposal{}, s.Generation(), ObserveIntegrated, ErrInvalidSchedule},
		{"foreign", s, foreign, s.Generation(), ObserveIntegrated, ErrScheduleContextMismatch},
		{"unknown", s, unknown, s.Generation(), ObserveIntegrated, ErrScheduleConflict},
		{"wrong carrier", s, wrongCarrier, s.Generation(), ObserveIntegrated, ErrScheduleConflict},
		{"absent event", s, a, s.Generation(), 0, ErrInvalidSchedule},
		{"unknown event", s, a, s.Generation(), 99, ErrInvalidSchedule},
		{"zero generation", s, a, 0, ObserveIntegrated, ErrStaleSchedule},
		{"old generation", integrated, a, s.Generation(), ObserveIntegrated, ErrStaleSchedule},
		{"waiting integrated", s, b, s.Generation(), ObserveIntegrated, ErrScheduleConflict},
		{"waiting promoted", s, b, s.Generation(), ObservePromoted, ErrScheduleConflict},
		{"merged closure", integrated, a, integrated.Generation(), ObserveClosedUnmerged, ErrScheduleConflict},
		{"closed integration", closed, b, closed.Generation(), ObserveIntegrated, ErrScheduleConflict},
		{"closed promotion", closed, b, closed.Generation(), ObservePromoted, ErrScheduleConflict},
		{"promoted closure", promoted, a, promoted.Generation(), ObserveClosedUnmerged, ErrScheduleConflict},
		{"promoted integration", promoted, a, promoted.Generation(), ObserveIntegrated, ErrScheduleConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := tc.s.Observe(tc.p, tc.generation, tc.event)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(next, tc.s) {
				t.Fatalf("invalid observation changed state: %v", err)
			}
		})
	}
	s.generation = ScheduleGeneration(math.MaxUint64)
	for _, event := range []ScheduleObservation{ObserveIntegrated, ObservePromoted, ObserveClosedUnmerged} {
		next, err := s.Observe(a, s.Generation(), event)
		if !errors.Is(err, ErrScheduleGenerationExhausted) || !reflect.DeepEqual(next, s) {
			t.Fatalf("overflow during %v mutated schedule: %v", event, err)
		}
	}
	if next := schedulingObserve(t, s, b, ObserveClosedUnmerged); next.Generation() != s.Generation() {
		t.Fatal("waiting closure needlessly increments exhausted generation")
	}
}

func TestScheduleAdmissionAndEligibility(t *testing.T) {
	s, a, b := schedulingPair(t)
	if s.IsZero() || s.ProjectID() != "project" || s.SuiteID() != "suite" || s.Generation() != 1 {
		t.Fatalf("constructed schedule lost identity or initial generation: %+v", s)
	}
	if _, ok := s.Active(); ok || s.CanPromote(a, s.Generation()) {
		t.Fatal("empty schedule authorized a proposal")
	}
	unchanged, err := s.Admit(a, false)
	if err != nil || len(unchanged.Entries()) != 0 || unchanged.Generation() != s.Generation() {
		t.Fatal("implementation-only observation consumed the position")
	}
	first, err := s.Admit(a, true)
	if err != nil {
		t.Fatal(err)
	}
	active, ok := first.Active()
	if !ok || active.ProposalID() != "a" || active.Carrier() != "carrier-a" || active.State() != ScheduleActive || !first.CanPromote(a, first.Generation()) {
		t.Fatal("first admitted contract change did not become eligible active entry")
	}
	if first.Generation() == s.Generation() || first.CanPromote(a, s.Generation()) || first.CanPromote(a, 0) {
		t.Fatal("active admission did not fence previous work")
	}
	next, err := first.Admit(b, true)
	if err != nil || !next.CanPromote(a, next.Generation()) || next.CanPromote(b, next.Generation()) || next.Generation() != first.Generation() {
		t.Fatal("waiting admission changed active authority")
	}
	entries := next.Entries()
	if len(entries) != 2 || entries[0].ProposalID() != "a" || entries[1].ProposalID() != "b" || entries[1].State() != ScheduleWaiting {
		t.Fatalf("admission order not retained: %+v", entries)
	}
	entries[0] = ScheduleEntry{}
	if next.Entries()[0].ProposalID() != "a" || len(s.Entries()) != 0 || len(first.Entries()) != 1 {
		t.Fatal("returned entries or later admission mutated earlier snapshot")
	}
	duplicate, err := next.Admit(a, true)
	if err != nil || len(duplicate.Entries()) != 2 || duplicate.Generation() != next.Generation() {
		t.Fatal("duplicate admission created another position")
	}
	revised := schedulingProposal(t, "project", "suite", "a", "carrier-a", "r2")
	if !next.CanPromote(revised, next.Generation()) {
		t.Fatal("scheduling incorrectly binds proposal revision instead of stable carrier")
	}
	if (Schedule{}).CanPromote(a, 0) || !(Schedule{}).IsZero() {
		t.Fatal("zero schedule became authority")
	}
}
