package contract

import (
	"errors"
	"reflect"
	"testing"
)

func scheduleEntryInputs(s Schedule) []ScheduleEntryInput {
	var inputs []ScheduleEntryInput
	for _, entry := range s.Entries() {
		inputs = append(inputs, ScheduleEntryInput{ProposalID: entry.ProposalID(), Carrier: entry.Carrier(), State: entry.State()})
	}
	return inputs
}

func TestReconstituteScheduleBehavesLikeTheOriginal(t *testing.T) {
	s, a, b := schedulingPair(t)
	c := schedulingProposal(t, "project", "suite", "c", "carrier-c", "r1")
	proposals := []Proposal{a, b, c}
	admitted := schedulingAdmit(t, schedulingAdmit(t, schedulingAdmit(t, s, a), b), c)
	promoted := schedulingObserve(t, admitted, a, ObservePromoted)
	closed := schedulingObserve(t, promoted, b, ObserveClosedUnmerged)
	drained := schedulingObserve(t, closed, c, ObservePromoted)
	for name, original := range map[string]Schedule{"empty": s, "queued": admitted, "after promotion": promoted, "after closure": closed, "drained": drained} {
		t.Run(name, func(t *testing.T) {
			rebuilt, err := ReconstituteSchedule(original.ProjectID(), original.SuiteID(), original.Generation(), scheduleEntryInputs(original))
			if err != nil {
				t.Fatalf("stored schedule did not reconstitute: %v", err)
			}
			if rebuilt.ProjectID() != original.ProjectID() || rebuilt.SuiteID() != original.SuiteID() || rebuilt.Generation() != original.Generation() ||
				!reflect.DeepEqual(rebuilt.Entries(), original.Entries()) {
				t.Fatal("rebuilt schedule lost identity, generation, or entries")
			}
			wantActive, wantOK := original.Active()
			if gotActive, gotOK := rebuilt.Active(); gotOK != wantOK || gotActive != wantActive {
				t.Fatal("rebuilt schedule changed the active entry")
			}
			for _, p := range proposals {
				if rebuilt.CanPromote(p) != original.CanPromote(p) {
					t.Fatalf("CanPromote(%s) differs", p.Current().Binding().Reference().ProposalID)
				}
				for _, observation := range []ScheduleObservation{ObserveClosedUnmerged, ObservePromoted} {
					want, wantErr := original.Observe(p, observation)
					got, gotErr := rebuilt.Observe(p, observation)
					if !errors.Is(gotErr, wantErr) || (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(got, want) {
						t.Fatalf("Observe(%s, %v) differs: got %v, want %v", p.Current().Binding().Reference().ProposalID, observation, gotErr, wantErr)
					}
				}
				want, wantErr := original.Admit(p, true)
				got, gotErr := rebuilt.Admit(p, true)
				if (wantErr == nil) != (gotErr == nil) || !reflect.DeepEqual(got, want) {
					t.Fatalf("Admit(%s) differs: got %v, want %v", p.Current().Binding().Reference().ProposalID, gotErr, wantErr)
				}
			}
			late := schedulingProposal(t, "project", "suite", "late", "carrier-late", "r1")
			if want, got := schedulingAdmit(t, original, late), schedulingAdmit(t, rebuilt, late); !reflect.DeepEqual(got, want) {
				t.Fatal("admission after reconstitution differs")
			}
		})
	}
}

func TestReconstituteScheduleRejectsInconsistentInput(t *testing.T) {
	waiting := func(id ProposalID, carrier ApprovalCarrierID) ScheduleEntryInput {
		return ScheduleEntryInput{ProposalID: id, Carrier: carrier, State: ScheduleWaiting}
	}
	active := func(id ProposalID, carrier ApprovalCarrierID) ScheduleEntryInput {
		return ScheduleEntryInput{ProposalID: id, Carrier: carrier, State: ScheduleActive}
	}
	for name, tc := range map[string]struct {
		project    ProjectID
		suite      SuiteID
		generation ScheduleGeneration
		entries    []ScheduleEntryInput
	}{
		"blank project":         {" ", "suite", 1, nil},
		"blank suite":           {"project", "", 1, nil},
		"zero generation":       {"project", "suite", 0, nil},
		"negative generation":   {"project", "suite", -1, nil},
		"two active entries":    {"project", "suite", 2, []ScheduleEntryInput{active("a", "ca"), active("b", "cb")}},
		"duplicate proposal id": {"project", "suite", 2, []ScheduleEntryInput{active("a", "ca"), waiting("a", "cb")}},
		"duplicate carrier":     {"project", "suite", 2, []ScheduleEntryInput{active("a", "ca"), waiting("b", "ca")}},
		"blank proposal id":     {"project", "suite", 2, []ScheduleEntryInput{waiting(" ", "ca")}},
		"blank carrier":         {"project", "suite", 2, []ScheduleEntryInput{waiting("a", "")}},
		"absent state":          {"project", "suite", 2, []ScheduleEntryInput{{ProposalID: "a", Carrier: "ca"}}},
		"unknown state":         {"project", "suite", 2, []ScheduleEntryInput{{ProposalID: "a", Carrier: "ca", State: 99}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ReconstituteSchedule(tc.project, tc.suite, tc.generation, tc.entries)
			if !errors.Is(err, ErrInvalidSchedule) || !got.IsZero() {
				t.Fatalf("inconsistent schedule accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestReconstituteScheduleDoesNotAliasItsInput(t *testing.T) {
	inputs := []ScheduleEntryInput{{ProposalID: "a", Carrier: "ca", State: ScheduleActive}}
	s, err := ReconstituteSchedule("project", "suite", 3, inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputs[0].State = ScheduleClosed
	if active, ok := s.Active(); !ok || active.ProposalID() != "a" {
		t.Fatal("mutating the input changed the reconstituted schedule")
	}
}
