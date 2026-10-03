package contract

import (
	"cmp"
	"slices"
)

func consentDataOf(c Consent) *consentData {
	d := &consentData{Project: c.project, Suite: c.suite, Proposal: c.proposal, Operations: cloneCheckpointOperations(c.operations)}
	for _, r := range c.results {
		d.Results = append(d.Results, *resultDataOf(r))
	}
	for key, state := range c.states {
		d.States = append(d.States, consentStateData{key.revision, principalDataOf(key.actor), bindingDataOf(state.binding), state.active, state.order})
	}
	slices.SortFunc(d.States, func(a, b consentStateData) int {
		if n := cmp.Compare(a.Revision, b.Revision); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Actor.ID, b.Actor.ID); n != 0 {
			return n
		}
		return cmp.Compare(a.Actor.Kind, b.Actor.Kind)
	})
	return d
}

func (d *consentData) restore() (Consent, error) {
	if d == nil {
		return Consent{}, nil
	}
	c, err := NewConsent(d.Project, d.Suite, d.Proposal)
	if err != nil {
		return Consent{}, err
	}
	type stateFact struct {
		active bool
		order  CommandOrder
	}
	facts := map[consentKey]stateFact{}
	orders := map[consentKey]map[CommandOrder]bool{}
	sources := map[SourceCommandID]Command{}
	for _, raw := range d.Results {
		r, err := raw.restore()
		if err != nil {
			return Consent{}, err
		}
		command := r.Command()
		if r.Duplicate() || !c.contains(command.Reference()) || sources[command.SourceCommandID()].OperationID() != "" || c.operations[command.OperationID()] != "" {
			return Consent{}, checkpointProblem("consent receipt")
		}
		sources[command.SourceCommandID()] = command
		c.results = append(c.results, r)
		c.operations[command.OperationID()] = command.SourceCommandID()
		key := consentKey{command.Reference().RevisionID, command.Actor()}
		if orders[key] == nil {
			orders[key] = map[CommandOrder]bool{}
		}
		if r.Outcome() != ConsentRejected && orders[key][command.Order()] {
			return Consent{}, checkpointProblem("consent reused receipt order")
		}
		orders[key][command.Order()] = true
		if r.Outcome() == ConsentRejected {
			continue
		}
		previous := facts[key]
		if command.Actor().Kind() != Human || command.Order() <= previous.order || (r.Outcome() == ConsentRevoked && !previous.active) || (r.Outcome() == ConsentNoActiveApproval && previous.active) {
			return Consent{}, checkpointProblem("consent result")
		}
		facts[key] = stateFact{r.Outcome() == ConsentApproved, command.Order()}
	}
	bindings := map[ProposalRevisionID]ApprovalBinding{}
	for _, raw := range d.States {
		actor, err := raw.Actor.restore()
		if err != nil {
			return Consent{}, err
		}
		binding, err := raw.Binding.restore()
		if err != nil {
			return Consent{}, err
		}
		key := consentKey{raw.Revision, actor}
		fact, exists := facts[key]
		if !exists || raw.Order == 0 || raw.Order != fact.order || raw.Active != fact.active || !c.contains(binding.Reference()) || binding.Reference().RevisionID != raw.Revision || !c.states[key].binding.IsZero() {
			return Consent{}, checkpointProblem("consent state")
		}
		if old, exists := bindings[raw.Revision]; exists && !old.Equal(binding) {
			return Consent{}, checkpointProblem("consent revision binding")
		}
		bindings[raw.Revision] = binding
		c.states[key] = consentState{binding: binding, active: raw.Active, order: raw.Order}
	}
	if len(c.states) != len(facts) {
		return Consent{}, checkpointProblem("missing consent state")
	}
	if err := restoreCheckpointOperations(c.operations, d.Operations, sources); err != nil {
		return Consent{}, err
	}
	c.operations = cloneCheckpointOperations(d.Operations)
	return c, nil
}

func restoreCheckpointOperations[T any](original, operations map[OperationID]SourceCommandID, sources map[SourceCommandID]T) error {
	if operations == nil {
		return checkpointProblem("operation map")
	}
	for operation, source := range original {
		if operations[operation] != source {
			return checkpointProblem("original operation")
		}
	}
	for operation, source := range operations {
		if !validCheckpointID(string(operation)) || !validCheckpointID(string(source)) {
			return checkpointProblem("operation identity")
		}
		if _, exists := sources[source]; !exists {
			return checkpointProblem("operation source")
		}
	}
	return nil
}

func priorityCommandDataOf(c PriorityCommand) *priorityCommandData {
	return &priorityCommandData{c.OperationID(), c.SourceCommandID(), principalDataOf(c.Actor()), c.ProjectID(), c.SuiteID(), c.ProposalID(), c.Carrier(), c.Order()}
}
func (d *priorityCommandData) restore() (PriorityCommand, error) {
	if d == nil {
		return PriorityCommand{}, nil
	}
	actor, err := d.Actor.restore()
	if err != nil {
		return PriorityCommand{}, err
	}
	return NewPriorityCommand(PriorityCommandInput{OperationID: d.OperationID, SourceCommandID: d.SourceCommandID, Actor: actor, ProjectID: d.Project, SuiteID: d.Suite, ProposalID: d.Proposal, Carrier: d.Carrier, Order: d.Order})
}
func priorityResultDataOf(r PriorityResult) priorityResultData {
	return priorityResultData{*priorityCommandDataOf(r.Command()), r.Outcome(), r.Reason(), r.Duplicate()}
}
func (d priorityResultData) restore() (PriorityResult, error) {
	c, err := d.Command.restore()
	if err != nil {
		return PriorityResult{}, err
	}
	if d.Outcome < PriorityRequested || d.Outcome > PriorityRejected || d.Reason > PriorityReasonObsoleteCommand {
		return PriorityResult{}, ErrInvalidCheckpoint
	}
	if d.Outcome == PriorityRejected {
		if d.Reason == PriorityReasonNone {
			return PriorityResult{}, ErrInvalidCheckpoint
		}
	} else if d.Reason != PriorityReasonNone || c.Actor().Kind() != Human {
		return PriorityResult{}, ErrInvalidCheckpoint
	}
	return PriorityResult{command: c, outcome: d.Outcome, reason: d.Reason, duplicate: d.Duplicate}, nil
}
func scheduleDataOf(s Schedule) *scheduleData {
	d := &scheduleData{Project: s.project, Suite: s.suite, Generation: s.generation, Operations: cloneCheckpointOperations(s.operations), Order: s.order}
	for _, entry := range s.entries {
		d.Entries = append(d.Entries, scheduleEntryData{entry.proposal, entry.carrier, entry.state})
	}
	if pending, present := s.PendingTransfer(); present {
		d.Pending = priorityCommandDataOf(pending)
	}
	for _, result := range s.results {
		d.Results = append(d.Results, priorityResultDataOf(result))
	}
	return d
}
func (d *scheduleData) restore() (Schedule, error) {
	if d == nil {
		return Schedule{}, nil
	}
	s, err := NewSchedule(d.Project, d.Suite)
	if err != nil {
		return Schedule{}, err
	}
	if d.Generation == 0 {
		return Schedule{}, ErrInvalidSchedule
	}
	s.generation = d.Generation
	proposals := map[ProposalID]bool{}
	carriers := map[ApprovalCarrierID]bool{}
	active := 0
	for _, entry := range d.Entries {
		if !validCheckpointID(string(entry.Proposal)) || !validCheckpointID(string(entry.Carrier)) || proposals[entry.Proposal] || carriers[entry.Carrier] || entry.State < ScheduleWaiting || entry.State > SchedulePromoted {
			return Schedule{}, checkpointProblem("schedule entry")
		}
		proposals[entry.Proposal] = true
		carriers[entry.Carrier] = true
		if entry.State == ScheduleActive || entry.State == ScheduleIntegratedPending {
			active++
		}
		s.entries = append(s.entries, ScheduleEntry{entry.Proposal, entry.Carrier, entry.State})
	}
	if active > 1 {
		return Schedule{}, checkpointProblem("multiple active schedule entries")
	}
	sources := map[SourceCommandID]PriorityCommand{}
	var order CommandOrder
	orders := map[CommandOrder]bool{}
	for _, raw := range d.Results {
		r, err := raw.restore()
		if err != nil {
			return Schedule{}, err
		}
		command := r.Command()
		if r.Duplicate() || command.ProjectID() != s.project || command.SuiteID() != s.suite || sources[command.SourceCommandID()].OperationID() != "" || s.operations[command.OperationID()] != "" {
			return Schedule{}, checkpointProblem("priority receipt")
		}
		if r.Outcome() != PriorityRejected {
			if command.Order() <= order || orders[command.Order()] || s.entryIndex(command.ProposalID(), command.Carrier()) < 0 {
				return Schedule{}, checkpointProblem("priority order or entry")
			}
			order = command.Order()
		}
		orders[command.Order()] = true
		sources[command.SourceCommandID()] = command
		s.results = append(s.results, r)
		s.operations[command.OperationID()] = command.SourceCommandID()
	}
	if order != d.Order {
		return Schedule{}, checkpointProblem("priority watermark")
	}
	s.order = order
	if err := restoreCheckpointOperations(s.operations, d.Operations, sources); err != nil {
		return Schedule{}, err
	}
	s.operations = cloneCheckpointOperations(d.Operations)
	if d.Pending != nil {
		pending, err := d.Pending.restore()
		if err != nil {
			return Schedule{}, err
		}
		if pending.ProjectID() != s.project || pending.SuiteID() != s.suite || active != 1 {
			return Schedule{}, checkpointProblem("pending priority context")
		}
		index := s.entryIndex(pending.ProposalID(), pending.Carrier())
		if index < 0 || s.entries[index].state != ScheduleWaiting {
			return Schedule{}, checkpointProblem("pending priority target")
		}
		found := false
		for _, r := range s.results {
			if r.Command() == pending && r.Outcome() == PriorityRequested {
				found = true
			}
		}
		if !found {
			return Schedule{}, checkpointProblem("pending priority receipt")
		}
		s.pending = pending
	}
	return s, nil
}
