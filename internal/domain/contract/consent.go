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
		return Consent{}, ErrInvalidConsent
	}
	return Consent{
		project: project, suite: suite, proposal: proposal,
		states: map[consentKey]consentState{}, operations: map[OperationID]SourceCommandID{},
	}, nil
}

func (c Consent) Apply(proposal Proposal, governing Policy, command Command) (Consent, CommandResult, error) {
	if command.OperationID() == "" {
		return c, CommandResult{}, ErrInvalidCommand
	}
	if c.project == "" || proposal.IsZero() || governing.ProjectID() != c.project || !c.contains(proposal.Current().Binding().Reference()) || !c.contains(command.Reference()) {
		return c, CommandResult{}, ErrInvalidConsent
	}
	observedSource, knownOperation := c.operations[command.OperationID()]
	if knownOperation && observedSource != command.SourceCommandID() {
		return c.conflict(command)
	}
	for _, original := range c.results {
		if original.command.SourceCommandID() != command.SourceCommandID() {
			continue
		}
		if original.command.Actor() != command.Actor() {
			return c.conflict(command)
		}
		next := c
		if !knownOperation {
			next.operations = maps.Clone(c.operations)
			next.operations[command.OperationID()] = command.SourceCommandID()
		}
		original.duplicate = true
		return next, original, nil
	}
	var revision ProposalRevision
	var err error
	if command.Action() == ApproveConsent {
		revision, err = proposal.Resolve(command.Reference(), command.Carrier())
	} else {
		revision, err = proposal.Lookup(command.Reference(), command.Carrier())
	}
	if err != nil {
		reason := ConsentReasonContextMismatch
		switch {
		case errors.Is(err, ErrUnknownRevision):
			reason = ConsentReasonUnknownRevision
		case errors.Is(err, ErrSupersededRevision):
			reason = ConsentReasonSupersededRevision
		}
		return c.reject(command, reason)
	}
	if command.Action() == ApproveConsent {
		if !governing.CanApprove(command.Actor()) {
			return c.reject(command, ConsentReasonUnauthorized)
		}
		if revision.Binding().PolicyRevisionID() != governing.RevisionID() {
			return c.reject(command, ConsentReasonPolicyMismatch)
		}
	} else if !governing.CanRevoke(command.Actor()) {
		return c.reject(command, ConsentReasonUnauthorized)
	}
	key := consentKey{command.Reference().RevisionID, command.Actor()}
	previous := c.states[key]
	if command.Order() <= previous.order {
		return c.reject(command, ConsentReasonObsoleteCommand)
	}
	for _, receipt := range c.results {
		observed := receipt.command
		if observed.Reference().RevisionID == key.revision && observed.Actor() == key.actor && observed.Order() == command.Order() {
			return c.reject(command, ConsentReasonObsoleteCommand)
		}
	}
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
	return next, result, nil
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

func (c Consent) reject(command Command, reason ConsentReason) (Consent, CommandResult, error) {
	result := CommandResult{command: command, outcome: ConsentRejected, reason: reason}
	return c.record(result), result, nil
}

func (c Consent) conflict(command Command) (Consent, CommandResult, error) {
	// Existing immutable identity facts determine this conflict on every retry.
	// Do not overwrite the original receipt or append a competing identity record.
	return c, CommandResult{command: command, outcome: ConsentRejected, reason: ConsentReasonCommandConflict}, nil
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
		return CommandResult{}, ErrInvalidCommand
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
		return Consent{}, ErrInvalidConsent
	}
	reference := proposal.Current().Binding().Reference()
	c, err := NewConsent(reference.ProjectID, reference.SuiteID, reference.ProposalID)
	if err != nil {
		return Consent{}, err
	}
	invalid := func(detail string) (Consent, error) {
		return Consent{}, fmt.Errorf("%w: %s", ErrInvalidConsent, detail)
	}
	sources := map[SourceCommandID]struct{}{}
	for _, result := range results {
		command := result.command
		switch {
		case result.duplicate || !result.valid():
			return invalid("result is zero, duplicate, or a command conflict")
		case !c.contains(command.Reference()):
			return invalid("result belongs to another aggregate")
		}
		if _, repeated := sources[command.SourceCommandID()]; repeated {
			return invalid("source command recorded twice")
		}
		if _, repeated := c.operations[command.OperationID()]; repeated {
			return invalid("operation recorded twice")
		}
		if result.outcome != ConsentRejected {
			key := consentKey{command.Reference().RevisionID, command.Actor()}
			previous := c.states[key]
			revision, err := proposal.Lookup(command.Reference(), command.Carrier())
			if err != nil {
				return invalid("result targets an unknown revision or carrier")
			}
			approve := command.Action() == ApproveConsent
			if !approve && previous.active != (result.outcome == ConsentRevoked) {
				return invalid("revocation outcome contradicts the earlier approval")
			}
			if command.Order() <= previous.order {
				return invalid("command order does not increase for the actor and revision")
			}
			for _, earlier := range c.results {
				if earlier.command.Reference().RevisionID == key.revision && earlier.command.Actor() == key.actor && earlier.command.Order() == command.Order() {
					return invalid("command order repeats for the actor and revision")
				}
			}
			c.states[key] = consentState{binding: revision.Binding(), active: approve, order: command.Order()}
		}
		sources[command.SourceCommandID()] = struct{}{}
		c.operations[command.OperationID()] = command.SourceCommandID()
		c.results = append(c.results, result)
	}
	for operation, source := range aliases {
		if strings.TrimSpace(string(operation)) == "" {
			return invalid("alias without an operation id")
		}
		if _, known := sources[source]; !known {
			return invalid("alias points at an unknown source command")
		}
		if existing, mapped := c.operations[operation]; mapped && existing != source {
			return invalid("operation maps to two source commands")
		}
		c.operations[operation] = source
	}
	return c, nil
}
