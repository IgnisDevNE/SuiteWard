package contract

import (
	"errors"
	"slices"
	"strings"
)

var (
	ErrInvalidSchedule         = errors.New("invalid schedule")
	ErrScheduleContextMismatch = errors.New("schedule context mismatch")
	ErrScheduleConflict        = errors.New("schedule conflict")
)

// ScheduleGeneration counts changes to active eligibility. The per-Suite lock
// serializes every write, so it is bookkeeping rather than a concurrency fence.
type ScheduleGeneration int64

type ScheduleEntryState uint8

// ScheduleObservation is a trusted application fact, not a promotion decision.
type ScheduleObservation uint8

const (
	ObserveClosedUnmerged ScheduleObservation = iota + 1
	ObservePromoted
)

const (
	ScheduleWaiting ScheduleEntryState = iota + 1
	ScheduleActive
	ScheduleClosed
	SchedulePromoted
)

// ScheduleEntry retains stable proposal/carrier identity and admission order.
type ScheduleEntry struct {
	proposal ProposalID
	carrier  ApprovalCarrierID
	state    ScheduleEntryState
}

func (e ScheduleEntry) ProposalID() ProposalID     { return e.proposal }
func (e ScheduleEntry) Carrier() ApprovalCarrierID { return e.carrier }
func (e ScheduleEntry) State() ScheduleEntryState  { return e.state }

// Schedule is immutable local scheduling state for one project and Suite.
// Independent copies do not elect a durable winner; the application must commit
// the returned state and its authority context together.
type Schedule struct {
	project    ProjectID
	suite      SuiteID
	generation ScheduleGeneration
	entries    []ScheduleEntry
}

func NewSchedule(project ProjectID, suite SuiteID) (Schedule, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(suite)) == "" {
		return Schedule{}, ErrInvalidSchedule
	}
	return Schedule{project: project, suite: suite, generation: 1}, nil
}
func (s Schedule) IsZero() bool                   { return s.project == "" }
func (s Schedule) ProjectID() ProjectID           { return s.project }
func (s Schedule) SuiteID() SuiteID               { return s.suite }
func (s Schedule) Generation() ScheduleGeneration { return s.generation }

// Admit accepts a trusted classification. An implementation-only observation
// does not consume a position or withdraw a previously admitted entry.
func (s Schedule) Admit(proposal Proposal, contractChanging bool) (Schedule, error) {
	if err := s.checkProposal(proposal); err != nil {
		return s, err
	}
	revision := proposal.Current()
	for _, entry := range s.entries {
		if entry.proposal == revision.Binding().Reference().ProposalID {
			if entry.carrier != revision.Carrier() {
				return s, ErrScheduleConflict
			}
			return s, nil
		}
		if entry.carrier == revision.Carrier() {
			return s, ErrScheduleConflict
		}
	}
	if !contractChanging {
		return s, nil
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

// CanPromote checks local priority only. The caller independently checks the
// exact proposal revision, consent, integrated assessment and canonical state.
func (s Schedule) CanPromote(proposal Proposal) bool {
	if s.IsZero() || proposal.IsZero() {
		return false
	}
	ref := proposal.Current().Binding().Reference()
	active, ok := s.Active()
	return ok && ref.ProjectID == s.project && ref.SuiteID == s.suite && active.proposal == ref.ProposalID && active.carrier == proposal.Current().Carrier()
}
func (s Schedule) Active() (ScheduleEntry, bool) {
	for _, entry := range s.entries {
		if entry.state == ScheduleActive {
			return entry, true
		}
	}
	return ScheduleEntry{}, false
}
func (s Schedule) Entries() []ScheduleEntry { return slices.Clone(s.entries) }

// Observe records trusted closure or committed promotion facts. It never
// performs or authorizes the promotion it observes. Existing-baseline
// promotion can be observed without merging its independent approval carrier.
func (s Schedule) Observe(proposal Proposal, observation ScheduleObservation) (Schedule, error) {
	if err := s.checkProposal(proposal); err != nil {
		return s, err
	}
	if observation < ObserveClosedUnmerged || observation > ObservePromoted {
		return s, ErrInvalidSchedule
	}
	i := s.entryIndex(proposal.Current().Binding().Reference().ProposalID, proposal.Current().Carrier())
	if i < 0 {
		return s, ErrScheduleConflict
	}
	previous := s.entries[i].state
	active := previous == ScheduleActive
	var desired ScheduleEntryState
	switch observation {
	case ObserveClosedUnmerged:
		if previous != ScheduleActive && previous != ScheduleWaiting && previous != ScheduleClosed {
			return s, ErrScheduleConflict
		}
		desired = ScheduleClosed
	case ObservePromoted:
		if !active && previous != SchedulePromoted {
			return s, ErrScheduleConflict
		}
		desired = SchedulePromoted
	}
	if previous == desired {
		return s, nil
	}
	next := s
	next.entries = slices.Clone(s.entries)
	next.entries[i].state = desired
	if active {
		next.generation++
		for j := range next.entries {
			if next.entries[j].state == ScheduleWaiting {
				next.entries[j].state = ScheduleActive
				break
			}
		}
	}
	return next, nil
}

func (s Schedule) entryIndex(proposal ProposalID, carrier ApprovalCarrierID) int {
	for i, entry := range s.entries {
		if entry.proposal == proposal && entry.carrier == carrier {
			return i
		}
	}
	return -1
}

func (s Schedule) checkProposal(proposal Proposal) error {
	if s.IsZero() || proposal.IsZero() {
		return ErrInvalidSchedule
	}
	ref := proposal.Current().Binding().Reference()
	if ref.ProjectID != s.project || ref.SuiteID != s.suite {
		return ErrScheduleContextMismatch
	}
	return nil
}

// ScheduleEntryInput is one stored schedule entry.
type ScheduleEntryInput struct {
	ProposalID ProposalID
	Carrier    ApprovalCarrierID
	State      ScheduleEntryState
}

// ReconstituteSchedule rebuilds a schedule; at most one entry is active and
// proposal ids and carriers are unique.
func ReconstituteSchedule(project ProjectID, suite SuiteID, generation ScheduleGeneration, entries []ScheduleEntryInput) (Schedule, error) {
	s, err := NewSchedule(project, suite)
	if err != nil || generation < 1 {
		return Schedule{}, ErrInvalidSchedule
	}
	s.generation = generation
	proposals, carriers, active := map[ProposalID]struct{}{}, map[ApprovalCarrierID]struct{}{}, false
	for _, entry := range entries {
		if strings.TrimSpace(string(entry.ProposalID)) == "" || strings.TrimSpace(string(entry.Carrier)) == "" ||
			entry.State < ScheduleWaiting || entry.State > SchedulePromoted {
			return Schedule{}, ErrInvalidSchedule
		}
		if _, repeated := proposals[entry.ProposalID]; repeated {
			return Schedule{}, ErrInvalidSchedule
		}
		if _, repeated := carriers[entry.Carrier]; repeated {
			return Schedule{}, ErrInvalidSchedule
		}
		if entry.State == ScheduleActive {
			if active {
				return Schedule{}, ErrInvalidSchedule
			}
			active = true
		}
		proposals[entry.ProposalID], carriers[entry.Carrier] = struct{}{}, struct{}{}
		s.entries = append(s.entries, ScheduleEntry{proposal: entry.ProposalID, carrier: entry.Carrier, state: entry.State})
	}
	return s, nil
}
