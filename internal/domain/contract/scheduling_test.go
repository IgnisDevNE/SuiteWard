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
