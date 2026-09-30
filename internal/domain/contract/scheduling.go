package contract

import "slices"

type ScheduleGeneration uint64

type ScheduleEntryState uint8

const (
	ScheduleWaiting ScheduleEntryState = iota + 1
	ScheduleActive
	ScheduleIntegratedPending
	ScheduleClosed
	SchedulePromoted
)

type ScheduleEntry struct {
	proposal ProposalID
	carrier  ApprovalCarrierID
	state    ScheduleEntryState
}

func (e ScheduleEntry) ProposalID() ProposalID     { return e.proposal }
func (e ScheduleEntry) Carrier() ApprovalCarrierID { return e.carrier }
func (e ScheduleEntry) State() ScheduleEntryState  { return e.state }

type Schedule struct {
	project    ProjectID
	suite      SuiteID
	generation ScheduleGeneration
	entries    []ScheduleEntry
}

func NewSchedule(project ProjectID, suite SuiteID) (Schedule, error) {
	return Schedule{project: project, suite: suite, generation: 1}, nil
}
func (s Schedule) IsZero() bool                   { return s.project == "" }
func (s Schedule) ProjectID() ProjectID           { return s.project }
func (s Schedule) SuiteID() SuiteID               { return s.suite }
func (s Schedule) Generation() ScheduleGeneration { return s.generation }
func (s Schedule) Admit(proposal Proposal, contractChanging bool) (Schedule, error) {
	if !contractChanging {
		return s, nil
	}
	revision := proposal.Current()
	for _, entry := range s.entries {
		if entry.proposal == revision.Binding().Reference().ProposalID {
			return s, nil
		}
	}
	entry := ScheduleEntry{proposal: revision.Binding().Reference().ProposalID, carrier: revision.Carrier(), state: ScheduleWaiting}
	next := s
	if _, ok := s.Active(); !ok {
		entry.state = ScheduleActive
		next.generation++
	}
	next.entries = append(slices.Clone(s.entries), entry)
	return next, nil
}
func (s Schedule) CanPromote(proposal Proposal, expectedGeneration ScheduleGeneration) bool {
	if s.IsZero() || proposal.IsZero() || expectedGeneration == 0 || expectedGeneration != s.generation {
		return false
	}
	ref := proposal.Current().Binding().Reference()
	active, ok := s.Active()
	return ok && ref.ProjectID == s.project && ref.SuiteID == s.suite && active.proposal == ref.ProposalID && active.carrier == proposal.Current().Carrier()
}
func (s Schedule) Active() (ScheduleEntry, bool) {
	for _, entry := range s.entries {
		if entry.state == ScheduleActive || entry.state == ScheduleIntegratedPending {
			return entry, true
		}
	}
	return ScheduleEntry{}, false
}
func (s Schedule) Entries() []ScheduleEntry { return slices.Clone(s.entries) }
