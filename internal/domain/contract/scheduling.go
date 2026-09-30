package contract

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

func NewSchedule(project ProjectID, suite SuiteID) (Schedule, error) { return Schedule{}, nil }
func (s Schedule) IsZero() bool                                      { return s.project == "" }
func (s Schedule) ProjectID() ProjectID                              { return s.project }
func (s Schedule) SuiteID() SuiteID                                  { return s.suite }
func (s Schedule) Generation() ScheduleGeneration                    { return s.generation }
func (s Schedule) Admit(proposal Proposal, contractChanging bool) (Schedule, error) {
	return s, nil
}
func (s Schedule) CanPromote(proposal Proposal, expectedGeneration ScheduleGeneration) bool {
	return false
}
func (s Schedule) Active() (ScheduleEntry, bool) { return ScheduleEntry{}, false }
func (s Schedule) Entries() []ScheduleEntry      { return nil }
