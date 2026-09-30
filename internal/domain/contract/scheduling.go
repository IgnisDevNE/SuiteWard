package contract

import (
	"errors"
	"maps"
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

// ScheduleGeneration fences active eligibility, not every aggregate mutation.
// The application must also serialize waiting entries and command receipts.
type ScheduleGeneration uint64

// PriorityCommandInput carries authenticated facts and stable source ordering
// supplied by a trusted caller. Construction does not authenticate those facts.
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

// PriorityCommand requests scheduling priority without granting consent.
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

// PriorityResult is a historical processing receipt. PriorityRequested records
// the request, never successful remote withdrawal or completed transfer.
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

// ScheduleObservation is a trusted application fact, not a promotion decision.
type ScheduleObservation uint8

// TransferResolution is trusted reconciliation supplied by the application.
// Its value does not prove remote check withdrawal or the absence of a merge.
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
	pending    PriorityCommand
	results    []PriorityResult
	operations map[OperationID]SourceCommandID
	order      CommandOrder
}

func NewSchedule(project ProjectID, suite SuiteID) (Schedule, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(suite)) == "" {
		return Schedule{}, ErrInvalidSchedule
	}
	return Schedule{project: project, suite: suite, generation: 1, operations: map[OperationID]SourceCommandID{}}, nil
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
		if s.generation == ScheduleGeneration(math.MaxUint64) {
			return s, ErrScheduleGenerationExhausted
		}
		entry.state = ScheduleActive
		next.generation++
	}
	next.entries = append(slices.Clone(s.entries), entry)
	return next, nil
}

// CanPromote checks local priority only. The caller independently checks the
// exact proposal revision, consent, integrated assessment and canonical state.
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

// ResolveTransfer consumes an exact pending request and current generation.
// The application must first reconcile remote publications and current target
// inputs. A local activation alone never establishes readiness for integration.
func (s Schedule) ResolveTransfer(request OperationID, expectedGeneration ScheduleGeneration, resolution TransferResolution) (Schedule, error) {
	if s.IsZero() || strings.TrimSpace(string(request)) == "" || resolution < TransferUnresolved || resolution > FormerMerged {
		return s, ErrInvalidSchedule
	}
	if expectedGeneration == 0 || expectedGeneration != s.generation {
		return s, ErrStaleSchedule
	}
	if s.pending.OperationID() == "" || s.pending.OperationID() != request {
		return s, ErrScheduleConflict
	}
	if resolution == TransferUnresolved {
		return s, nil
	}
	active, _ := s.Active()
	former := s.entryIndex(active.proposal, active.carrier)
	target := s.entryIndex(s.pending.ProposalID(), s.pending.Carrier())
	if resolution == FormerUnmergedWithdrawn && (active.state != ScheduleActive || s.entries[target].state != ScheduleWaiting) {
		return s, ErrScheduleConflict
	}
	if s.generation == ScheduleGeneration(math.MaxUint64) {
		return s, ErrScheduleGenerationExhausted
	}
	next := s
	next.entries = slices.Clone(s.entries)
	next.pending = PriorityCommand{}
	next.generation++
	if resolution == FormerMerged {
		next.entries[former].state = ScheduleIntegratedPending
	} else {
		next.entries[former].state = ScheduleWaiting
		next.entries[target].state = ScheduleActive
	}
	return next, nil
}

// RequestPriority records a command under the supplied current governing policy.
// Replayed source commands return their original historical outcome.
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
	observedSource, knownOperation := s.operations[command.OperationID()]
	if knownOperation && observedSource != command.SourceCommandID() {
		return s.priorityConflict(command)
	}
	for _, original := range s.results {
		if original.command.SourceCommandID() != command.SourceCommandID() {
			continue
		}
		if original.command.Actor() != command.Actor() {
			return s.priorityConflict(command)
		}
		next := s
		if !knownOperation {
			next.operations = maps.Clone(s.operations)
			next.operations[command.OperationID()] = command.SourceCommandID()
		}
		original.duplicate = true
		return next, original, nil
	}
	if !governing.CanRequestPriority(command.Actor()) {
		return s.rejectPriority(command, PriorityReasonUnauthorized)
	}
	if command.Order() <= s.order {
		return s.rejectPriority(command, PriorityReasonObsoleteCommand)
	}
	for _, original := range s.results {
		if original.command.Order() == command.Order() {
			return s.rejectPriority(command, PriorityReasonObsoleteCommand)
		}
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
	next.operations = maps.Clone(s.operations)
	next.operations[result.command.OperationID()] = result.command.SourceCommandID()
	if result.outcome != PriorityRejected {
		next.order = result.command.Order()
	}
	return next
}

func (s Schedule) priorityConflict(command PriorityCommand) (Schedule, PriorityResult, error) {
	return s, PriorityResult{command: command, outcome: PriorityRejected, reason: PriorityReasonCommandConflict}, nil
}

func (s Schedule) rejectPriority(command PriorityCommand, reason PriorityReason) (Schedule, PriorityResult, error) {
	result := PriorityResult{command: command, outcome: PriorityRejected, reason: reason}
	return s.recordPriority(result), result, nil
}

// Observe records trusted integration, closure, or committed promotion facts.
// It never performs or authorizes the promotion it observes. Existing-baseline
// promotion can be observed without merging its independent approval carrier.
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
