package contract

import (
	"errors"
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
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(next, tc.schedule) || tc.schedule.CanPromote(tc.proposal) {
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
		if !errors.Is(err, ErrScheduleConflict) || !reflect.DeepEqual(next, s) || s.CanPromote(p) {
			t.Errorf("identity collision not rejected unchanged: %v", err)
		}
	}
	kept, err := s.Admit(a, false)
	if err != nil || !kept.CanPromote(a) {
		t.Fatal("false classification withdrew existing active entry")
	}
	spaced, err := NewSchedule(" project ", " suite ")
	if err != nil || spaced.ProjectID() != " project " || spaced.SuiteID() != " suite " {
		t.Fatal("accepted ID bytes were normalized")
	}
}

func TestScheduleClassificationCannotHideIdentityCollision(t *testing.T) {
	s, a, _ := schedulingPair(t)
	s = schedulingAdmit(t, s, a)
	for _, proposal := range []Proposal{
		schedulingProposal(t, "project", "suite", "a", "different", "r1"),
		schedulingProposal(t, "project", "suite", "different", "carrier-a", "r1"),
	} {
		for _, changing := range []bool{false, true} {
			next, err := s.Admit(proposal, changing)
			if !errors.Is(err, ErrScheduleConflict) || !reflect.DeepEqual(next, s) {
				t.Errorf("classification %v hid inconsistent admitted identity: %v", changing, err)
			}
		}
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
	next, err := s.Observe(p, event)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestScheduleObservations(t *testing.T) {
	s, a, b := schedulingPair(t)
	c := schedulingProposal(t, "project", "suite", "c", "carrier-c", "r1")
	s = schedulingAdmit(t, schedulingAdmit(t, schedulingAdmit(t, s, a), b), c)
	promoted := schedulingObserve(t, s, a, ObservePromoted)
	if !promoted.CanPromote(b) || promoted.CanPromote(a) || promoted.Entries()[0].State() != SchedulePromoted || promoted.Generation() == s.Generation() {
		t.Fatal("committed promotion did not advance to earliest waiting entry")
	}
	if again := schedulingObserve(t, promoted, a, ObservePromoted); !reflect.DeepEqual(again, promoted) {
		t.Fatal("repeated promotion changed queue")
	}
	closed := schedulingObserve(t, promoted, b, ObserveClosedUnmerged)
	if !closed.CanPromote(c) || closed.Entries()[1].State() != ScheduleClosed {
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
	if reopened := schedulingAdmit(t, empty, d); !reopened.CanPromote(d) {
		t.Fatal("new proposal did not take free position")
	}
	waitingClosed := schedulingObserve(t, s, b, ObserveClosedUnmerged)
	if waitingClosed.Generation() != s.Generation() || !waitingClosed.CanPromote(a) {
		t.Fatal("waiting closure invalidated active authority")
	}
	after := schedulingObserve(t, waitingClosed, a, ObservePromoted)
	if !after.CanPromote(c) {
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
	closed := schedulingObserve(t, s, b, ObserveClosedUnmerged)
	promoted := schedulingObserve(t, s, a, ObservePromoted)
	for _, tc := range []struct {
		name  string
		s     Schedule
		p     Proposal
		event ScheduleObservation
		want  error
	}{
		{"zero schedule", Schedule{}, a, ObservePromoted, ErrInvalidSchedule},
		{"zero proposal", s, Proposal{}, ObservePromoted, ErrInvalidSchedule},
		{"foreign", s, foreign, ObservePromoted, ErrScheduleContextMismatch},
		{"unknown", s, unknown, ObservePromoted, ErrScheduleConflict},
		{"wrong carrier", s, wrongCarrier, ObservePromoted, ErrScheduleConflict},
		{"absent event", s, a, 0, ErrInvalidSchedule},
		{"unknown event", s, a, 99, ErrInvalidSchedule},
		{"waiting promoted", s, b, ObservePromoted, ErrScheduleConflict},
		{"closed promotion", closed, b, ObservePromoted, ErrScheduleConflict},
		{"promoted closure", promoted, a, ObserveClosedUnmerged, ErrScheduleConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := tc.s.Observe(tc.p, tc.event)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(next, tc.s) {
				t.Fatalf("invalid observation changed state: %v", err)
			}
		})
	}
}

func TestScheduleAdmissionAndEligibility(t *testing.T) {
	s, a, b := schedulingPair(t)
	if s.IsZero() || s.ProjectID() != "project" || s.SuiteID() != "suite" || s.Generation() != 1 {
		t.Fatalf("constructed schedule lost identity or initial generation: %+v", s)
	}
	if _, ok := s.Active(); ok || s.CanPromote(a) {
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
	if !ok || active.ProposalID() != "a" || active.Carrier() != "carrier-a" || active.State() != ScheduleActive || !first.CanPromote(a) {
		t.Fatal("first admitted contract change did not become eligible active entry")
	}
	if first.Generation() == s.Generation() {
		t.Fatal("active admission did not advance the generation")
	}
	next, err := first.Admit(b, true)
	if err != nil || !next.CanPromote(a) || next.CanPromote(b) || next.Generation() != first.Generation() {
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
	if !next.CanPromote(revised) {
		t.Fatal("scheduling incorrectly binds proposal revision instead of stable carrier")
	}
	if (Schedule{}).CanPromote(a) || !(Schedule{}).IsZero() {
		t.Fatal("zero schedule became authority")
	}
}
