package contract

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

var ErrInvalidConsent = errors.New("invalid consent context")

type ConsentOutcome uint8

const (
	ConsentApproved ConsentOutcome = iota + 1
	ConsentRevoked
	ConsentNoActiveApproval
	ConsentRejected
)

type ConsentReason uint8

const (
	ConsentReasonNone ConsentReason = iota
	ConsentReasonUnauthorized
	ConsentReasonUnknownRevision
	ConsentReasonSupersededRevision
	ConsentReasonContextMismatch
	ConsentReasonPolicyMismatch
	ConsentReasonObsoleteCommand
	ConsentReasonCommandConflict
)

// CommandResult describes the immutable processing outcome, not current consent.
type CommandResult struct {
	command   Command
	outcome   ConsentOutcome
	reason    ConsentReason
	duplicate bool
}

func (r CommandResult) Command() Command        { return r.command }
func (r CommandResult) Outcome() ConsentOutcome { return r.outcome }
func (r CommandResult) Reason() ConsentReason   { return r.reason }
func (r CommandResult) Duplicate() bool         { return r.duplicate }

// Consent is an immutable, project/suite/proposal-scoped processing history.
// Callers must persist its returned state and command outcome together.
type Consent struct {
	project    ProjectID
	suite      SuiteID
	proposal   ProposalID
	results    []CommandResult
	states     map[consentKey]consentState
	operations map[OperationID]SourceCommandID
}

type consentKey struct {
	revision ProposalRevisionID
	actor    Principal
}

type consentState struct {
	binding ApprovalBinding
	active  bool
	order   CommandOrder
}

func NewConsent(project ProjectID, suite SuiteID, proposal ProposalID) (Consent, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(suite)) == "" || strings.TrimSpace(string(proposal)) == "" {
		return Consent{}, fmt.Errorf("%w: a consent names its project, suite and proposal", ErrInvalidConsent)
	}
	return Consent{
		project: project, suite: suite, proposal: proposal,
		states: map[consentKey]consentState{}, operations: map[OperationID]SourceCommandID{},
	}, nil
}

// Apply decides command against the proposal and the governing policy. The order of the checks is the contract: an
// identity already seen is replayed or conflicts first, so a repeat never depends on today's eligibility; then the
// revision, the actor's authority, the policy and the order of the actor's earlier commands, each naming its own reason.
func (c Consent) Apply(proposal Proposal, governing Policy, command Command) (Consent, CommandResult, error) {
	if command.OperationID() == "" {
		return c, CommandResult{}, fmt.Errorf("%w: a command names its operation", ErrInvalidCommand)
	}
	if c.project == "" || proposal.IsZero() || governing.ProjectID() != c.project || !c.contains(proposal.Current().Binding().Reference()) || !c.contains(command.Reference()) {
		return c, CommandResult{}, fmt.Errorf("%w: the proposal, the policy and the command belong to this consent", ErrInvalidConsent)
	}
	if next, result, seen := c.replay(command); seen {
		return next, result, nil
	}
	revision, reason, err := c.rejection(proposal, governing, command)
	if err != nil {
		return c, CommandResult{}, err
	}
	if reason != ConsentReasonNone {
		next, result := c.reject(command, reason)
		return next, result, nil
	}
	next, result := c.accept(command, revision)
	return next, result, nil
}

// replay answers a command whose operation or source command the history has seen: the original result for a repeat of
// its source command, and a conflict for an identity another source command or actor already holds. It reports false
// for a command that is new.
func (c Consent) replay(command Command) (Consent, CommandResult, bool) {
	observedSource, knownOperation := c.operations[command.OperationID()]
	if knownOperation && observedSource != command.SourceCommandID() {
		next, result := c.conflict(command)
		return next, result, true
	}
	for _, original := range c.results {
		if original.command.SourceCommandID() != command.SourceCommandID() {
			continue
		}
		if original.command.Actor() != command.Actor() {
			next, result := c.conflict(command)
			return next, result, true
		}
		next := c
		if !knownOperation {
			next.operations = maps.Clone(c.operations)
			next.operations[command.OperationID()] = command.SourceCommandID()
		}
		original.duplicate = true
		return next, original, true
	}
	return c, CommandResult{}, false
}

// rejection returns the revision command addresses, or the reason Apply records instead of consent: ConsentReasonNone
// when the command is eligible. The error is for a command the proposal cannot answer at all, which a Command built by
// NewCommand never is.
func (c Consent) rejection(proposal Proposal, governing Policy, command Command) (ProposalRevision, ConsentReason, error) {
	approve := command.Action() == ApproveConsent
	var revision ProposalRevision
	var err error
	if approve {
		revision, err = proposal.Resolve(command.Reference(), command.Carrier())
	} else {
		revision, err = proposal.Lookup(command.Reference(), command.Carrier())
	}
	switch {
	case err == nil:
	case errors.Is(err, ErrProposalContextMismatch):
		return ProposalRevision{}, ConsentReasonContextMismatch, nil
	case errors.Is(err, ErrUnknownRevision):
		return ProposalRevision{}, ConsentReasonUnknownRevision, nil
	case errors.Is(err, ErrSupersededRevision):
		return ProposalRevision{}, ConsentReasonSupersededRevision, nil
	default:
		return ProposalRevision{}, ConsentReasonNone, fmt.Errorf("%w: the proposal cannot answer the command's reference: %w", ErrInvalidConsent, err)
	}
	switch {
	case approve && !governing.CanApprove(command.Actor()), !approve && !governing.CanRevoke(command.Actor()):
		return revision, ConsentReasonUnauthorized, nil
	case approve && revision.Binding().PolicyRevisionID() != governing.RevisionID():
		return revision, ConsentReasonPolicyMismatch, nil
	case c.orderTaken(consentKey{command.Reference().RevisionID, command.Actor()}, command.Order()):
		return revision, ConsentReasonObsoleteCommand, nil
	}
	return revision, ConsentReasonNone, nil
}

// accept records an eligible command and moves the actor's state for the revision to it.
func (c Consent) accept(command Command, revision ProposalRevision) (Consent, CommandResult) {
	key := consentKey{command.Reference().RevisionID, command.Actor()}
	previous := c.states[key]
	active := command.Action() == ApproveConsent
	result := CommandResult{command: command, outcome: ConsentApproved}
	if !active {
		result.outcome = ConsentNoActiveApproval
		if previous.active {
			result.outcome = ConsentRevoked
		}
	}
	next := c.record(result)
	next.states = maps.Clone(c.states)
	next.states[key] = consentState{binding: revision.Binding(), active: active, order: command.Order()}
	return next, result
}

// orderTaken reports whether order cannot be the next command order of the actor for the revision: it does not exceed the
// order of the actor's state, or it equals the order of a stored result. A rejected result moves no state but keeps its
// order, so a later command cannot reuse it. Apply and ReconstituteConsent share it, so a history that Apply could not
// have produced does not reconstitute.
func (c Consent) orderTaken(key consentKey, order CommandOrder) bool {
	if order <= c.states[key].order {
		return true
	}
	return slices.ContainsFunc(c.results, func(stored CommandResult) bool {
		observed := stored.command
		return observed.Reference().RevisionID == key.revision && observed.Actor() == key.actor && observed.Order() == order
	})
}

// HasApproval reports consent eligibility only; it never authorizes promotion.
func (c Consent) HasApproval(proposal Proposal, governing Policy) bool {
	if c.project == "" || proposal.IsZero() || governing.ProjectID() != c.project || !c.contains(proposal.Current().Binding().Reference()) {
		return false
	}
	binding := proposal.Current().Binding()
	if binding.PolicyRevisionID() != governing.RevisionID() {
		return false
	}
	for key, state := range c.states {
		if key.revision == binding.Reference().RevisionID && state.active && governing.CanApprove(key.actor) && state.binding.Equal(binding) {
			return true
		}
	}
	return false
}

func (c Consent) Results() []CommandResult { return slices.Clone(c.results) }

func (c Consent) contains(reference ProposalReference) bool {
	return c.project == reference.ProjectID && c.suite == reference.SuiteID && c.proposal == reference.ProposalID
}

func (c Consent) record(result CommandResult) Consent {
	next := c
	next.results = append(slices.Clone(c.results), result)
	next.operations = maps.Clone(c.operations)
	next.operations[result.command.OperationID()] = result.command.SourceCommandID()
	return next
}

func (c Consent) reject(command Command, reason ConsentReason) (Consent, CommandResult) {
	result := CommandResult{command: command, outcome: ConsentRejected, reason: reason}
	return c.record(result), result
}

func (c Consent) conflict(command Command) (Consent, CommandResult) {
	// Existing immutable identity facts determine this conflict on every retry.
	// Do not overwrite the original receipt or append a competing identity record.
	return c, CommandResult{command: command, outcome: ConsentRejected, reason: ConsentReasonCommandConflict}
}

// valid reports whether the outcome and reason are a combination Apply can
// record: an approval is approved or rejected, a revocation is revoked,
// no-active or rejected, only a rejection carries a reason, and never a
// command conflict, which is returned but not stored.
func (r CommandResult) valid() bool {
	if r.command.OperationID() == "" || r.outcome < ConsentApproved || r.outcome > ConsentRejected {
		return false
	}
	if (r.command.Action() == ApproveConsent) != (r.outcome == ConsentApproved) && r.outcome != ConsentRejected {
		return false
	}
	if r.outcome == ConsentRejected {
		return r.reason >= ConsentReasonUnauthorized && r.reason <= ConsentReasonObsoleteCommand
	}
	return r.reason == ConsentReasonNone
}

// ReconstituteCommandResult rebuilds a stored, non-duplicate command result.
func ReconstituteCommandResult(command Command, outcome ConsentOutcome, reason ConsentReason) (CommandResult, error) {
	result := CommandResult{command: command, outcome: outcome, reason: reason}
	if !result.valid() {
		return CommandResult{}, fmt.Errorf("%w: the outcome and reason do not fit the command", ErrInvalidCommand)
	}
	return result, nil
}

// ReconstituteConsent rebuilds consent for proposal's aggregate from stored
// results in their original order plus operation aliases. It derives the
// per-revision, per-actor state exactly as Apply produced it and rejects
// inconsistent input (foreign aggregate, duplicate or conflict results,
// non-increasing order for an actor and revision, unknown revisions, aliases
// pointing at unknown source commands).
func ReconstituteConsent(proposal Proposal, results []CommandResult, aliases map[OperationID]SourceCommandID) (Consent, error) {
	if proposal.IsZero() {
		return Consent{}, fmt.Errorf("%w: no proposal to rebuild consent for", ErrInvalidConsent)
	}
	reference := proposal.Current().Binding().Reference()
	c, err := NewConsent(reference.ProjectID, reference.SuiteID, reference.ProposalID)
	if err != nil {
		return Consent{}, err
	}
	sources := map[SourceCommandID]struct{}{}
	for _, result := range results {
		if err := c.restore(proposal, result, sources); err != nil {
			return Consent{}, err
		}
	}
	if err := c.restoreAliases(sources, aliases); err != nil {
		return Consent{}, err
	}
	return c, nil
}

func invalidConsent(detail string) error { return fmt.Errorf("%w: %s", ErrInvalidConsent, detail) }

// restore appends one stored result to c and, unless it was rejected, moves the actor's state to it.
func (c *Consent) restore(proposal Proposal, result CommandResult, sources map[SourceCommandID]struct{}) error {
	command := result.command
	switch {
	case result.duplicate || !result.valid():
		return invalidConsent("result is zero, duplicate, or a command conflict")
	case !c.contains(command.Reference()):
		return invalidConsent("result belongs to another aggregate")
	}
	if _, repeated := sources[command.SourceCommandID()]; repeated {
		return invalidConsent("source command recorded twice")
	}
	if _, repeated := c.operations[command.OperationID()]; repeated {
		return invalidConsent("operation recorded twice")
	}
	if result.outcome != ConsentRejected {
		if err := c.restoreState(proposal, result); err != nil {
			return err
		}
	}
	sources[command.SourceCommandID()] = struct{}{}
	c.operations[command.OperationID()] = command.SourceCommandID()
	c.results = append(c.results, result)
	return nil
}

// restoreState checks that an accepted result is what Apply would have recorded after the results restored so far,
// and moves the actor's state for its revision to it.
func (c *Consent) restoreState(proposal Proposal, result CommandResult) error {
	command := result.command
	key := consentKey{command.Reference().RevisionID, command.Actor()}
	revision, err := proposal.Lookup(command.Reference(), command.Carrier())
	if err != nil {
		return invalidConsent("result targets an unknown revision or carrier")
	}
	approve := command.Action() == ApproveConsent
	if !approve && c.states[key].active != (result.outcome == ConsentRevoked) {
		return invalidConsent("revocation outcome contradicts the earlier approval")
	}
	if c.orderTaken(key, command.Order()) {
		return invalidConsent("command order does not increase for the actor and revision, or repeats a stored one")
	}
	c.states[key] = consentState{binding: revision.Binding(), active: approve, order: command.Order()}
	return nil
}

// restoreAliases adds the operation ids that replayed a stored source command.
func (c *Consent) restoreAliases(sources map[SourceCommandID]struct{}, aliases map[OperationID]SourceCommandID) error {
	for operation, source := range aliases {
		if strings.TrimSpace(string(operation)) == "" {
			return invalidConsent("alias without an operation id")
		}
		if _, known := sources[source]; !known {
			return invalidConsent("alias points at an unknown source command")
		}
		if existing, mapped := c.operations[operation]; mapped && existing != source {
			return invalidConsent("operation maps to two source commands")
		}
		c.operations[operation] = source
	}
	return nil
}
