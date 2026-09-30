package contract

import (
	"errors"
	"math"
	"slices"
	"strings"
)

var (
	ErrInvalidSchedule             = errors.New("invalid schedule")
	ErrInvalidPriorityCommand      = errors.New("invalid priority command")
	ErrScheduleContextMismatch     = errors.New("schedule context mismatch")
	ErrScheduleConflict            = errors.New("schedule conflict")
	ErrStaleSchedule               = errors.New("stale schedule generation")
	ErrScheduleGenerationExhausted = errors.New("schedule generation exhausted")
)

type ScheduleGeneration uint64

type PriorityCommandInput struct {
	OperationID     OperationID
	SourceCommandID SourceCommandID
	Actor           Principal
	ProjectID       ProjectID
	SuiteID         SuiteID
	ProposalID      ProposalID
	Carrier         ApprovalCarrierID
	Order           CommandOrder
}

type PriorityCommand struct{ input PriorityCommandInput }

func NewPriorityCommand(input PriorityCommandInput) (PriorityCommand, error) {
	for _, id := range []string{string(input.OperationID), string(input.SourceCommandID), string(input.ProjectID), string(input.SuiteID), string(input.ProposalID), string(input.Carrier)} {
		if strings.TrimSpace(id) == "" {
			return PriorityCommand{}, ErrInvalidPriorityCommand
		}
	}
	if input.Actor.ID() == "" || input.Order == 0 {
		return PriorityCommand{}, ErrInvalidPriorityCommand
	}
	return PriorityCommand{input: input}, nil
}
func (c PriorityCommand) OperationID() OperationID         { return c.input.OperationID }
func (c PriorityCommand) SourceCommandID() SourceCommandID { return c.input.SourceCommandID }
func (c PriorityCommand) Actor() Principal                 { return c.input.Actor }
func (c PriorityCommand) ProjectID() ProjectID             { return c.input.ProjectID }
func (c PriorityCommand) SuiteID() SuiteID                 { return c.input.SuiteID }
func (c PriorityCommand) ProposalID() ProposalID           { return c.input.ProposalID }
func (c PriorityCommand) Carrier() ApprovalCarrierID       { return c.input.Carrier }
func (c PriorityCommand) Order() CommandOrder              { return c.input.Order }

type PriorityOutcome uint8

const (
	PriorityRequested PriorityOutcome = iota + 1
	PriorityAlreadyActive
	PriorityRejected
)

type PriorityReason uint8

const (
	PriorityReasonNone PriorityReason = iota
	PriorityReasonUnauthorized
	PriorityReasonUnknownTarget
	PriorityReasonClosedTarget
	PriorityReasonMergedTarget
	PriorityReasonTransferPending
	PriorityReasonObsoleteCommand
	PriorityReasonCommandConflict
)

type PriorityResult struct {
	command   PriorityCommand
	outcome   PriorityOutcome
	reason    PriorityReason
	duplicate bool
}

func (r PriorityResult) Command() PriorityCommand { return r.command }
func (r PriorityResult) Outcome() PriorityOutcome { return r.outcome }
func (r PriorityResult) Reason() PriorityReason   { return r.reason }
func (r PriorityResult) Duplicate() bool          { return r.duplicate }

type ScheduleEntryState uint8

type ScheduleObservation uint8

type TransferResolution uint8

const (
	TransferUnresolved TransferResolution = iota + 1
	FormerUnmergedWithdrawn
	FormerMerged
)

const (
	ObserveIntegrated ScheduleObservation = iota + 1
	ObserveClosedUnmerged
	ObservePromoted
)

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
	pending    PriorityCommand
	results    []PriorityResult
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
func (s Schedule) Admit(proposal Proposal, contractChanging bool) (Schedule, error) {
	if err := s.checkProposal(proposal); err != nil {
		return s, err
	}
	if !contractChanging {
		return s, nil
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
	entry := ScheduleEntry{proposal: revision.Binding().Reference().ProposalID, carrier: revision.Carrier(), state: ScheduleWaiting}
	next := s
	if _, ok := s.Active(); !ok {
		if s.generation == ScheduleGeneration(math.MaxUint64) {
			return s, ErrScheduleGenerationExhausted
		}
		entry.state = ScheduleActive
		next.generation++
	}
	next.entries = append(slices.Clone(s.entries), entry)
	return next, nil
}
func (s Schedule) CanPromote(proposal Proposal, expectedGeneration ScheduleGeneration) bool {
	if s.IsZero() || proposal.IsZero() || expectedGeneration == 0 || expectedGeneration != s.generation || s.pending.OperationID() != "" {
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

func (s Schedule) PendingTransfer() (PriorityCommand, bool) {
	return s.pending, s.pending.OperationID() != ""
}
func (s Schedule) Results() []PriorityResult { return slices.Clone(s.results) }
func (s Schedule) ResolveTransfer(request OperationID, expectedGeneration ScheduleGeneration, resolution TransferResolution) (Schedule, error) {
	return s, nil
}
func (s Schedule) RequestPriority(governing Policy, command PriorityCommand) (Schedule, PriorityResult, error) {
	if command.OperationID() == "" {
		return s, PriorityResult{}, ErrInvalidPriorityCommand
	}
	if s.IsZero() {
		return s, PriorityResult{}, ErrInvalidSchedule
	}
	if governing.ProjectID() != s.project || command.ProjectID() != s.project || command.SuiteID() != s.suite {
		return s, PriorityResult{}, ErrScheduleContextMismatch
	}
	if !governing.CanRequestPriority(command.Actor()) {
		return s.rejectPriority(command, PriorityReasonUnauthorized)
	}
	i := s.entryIndex(command.ProposalID(), command.Carrier())
	if i < 0 {
		return s.rejectPriority(command, PriorityReasonUnknownTarget)
	}
	state := s.entries[i].state
	if state == ScheduleClosed {
		return s.rejectPriority(command, PriorityReasonClosedTarget)
	}
	if state == ScheduleIntegratedPending || state == SchedulePromoted {
		return s.rejectPriority(command, PriorityReasonMergedTarget)
	}
	if s.pending.OperationID() != "" {
		return s.rejectPriority(command, PriorityReasonTransferPending)
	}
	result := PriorityResult{command: command, outcome: PriorityAlreadyActive}
	if state == ScheduleActive {
		return s.recordPriority(result), result, nil
	}
	if s.generation == ScheduleGeneration(math.MaxUint64) {
		return s, PriorityResult{}, ErrScheduleGenerationExhausted
	}
	result.outcome = PriorityRequested
	next := s.recordPriority(result)
	next.pending = command
	next.generation++
	return next, result, nil
}

func (s Schedule) recordPriority(result PriorityResult) Schedule {
	next := s
	next.results = append(slices.Clone(s.results), result)
	return next
}

func (s Schedule) rejectPriority(command PriorityCommand, reason PriorityReason) (Schedule, PriorityResult, error) {
	result := PriorityResult{command: command, outcome: PriorityRejected, reason: reason}
	return s.recordPriority(result), result, nil
}

func (s Schedule) Observe(proposal Proposal, expectedGeneration ScheduleGeneration, observation ScheduleObservation) (Schedule, error) {
	if err := s.checkProposal(proposal); err != nil {
		return s, err
	}
	if observation < ObserveIntegrated || observation > ObservePromoted {
		return s, ErrInvalidSchedule
	}
	if expectedGeneration == 0 || expectedGeneration != s.generation {
		return s, ErrStaleSchedule
	}
	i := s.entryIndex(proposal.Current().Binding().Reference().ProposalID, proposal.Current().Carrier())
	if i < 0 {
		return s, ErrScheduleConflict
	}
	previous := s.entries[i].state
	active := previous == ScheduleActive || previous == ScheduleIntegratedPending
	if active && s.pending.OperationID() != "" {
		return s, ErrScheduleConflict
	}
	var desired ScheduleEntryState
	switch observation {
	case ObserveIntegrated:
		if !active {
			return s, ErrScheduleConflict
		}
		desired = ScheduleIntegratedPending
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
	if active && s.generation == ScheduleGeneration(math.MaxUint64) {
		return s, ErrScheduleGenerationExhausted
	}
	next := s
	next.entries = slices.Clone(s.entries)
	next.entries[i].state = desired
	if active {
		next.generation++
		if desired != ScheduleIntegratedPending {
			for j := range next.entries {
				if next.entries[j].state == ScheduleWaiting {
					next.entries[j].state = ScheduleActive
					break
				}
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
