package contract

import (
	"errors"
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
	project  ProjectID
	suite    SuiteID
	proposal ProposalID
	results  []CommandResult
	states   map[consentKey]consentState
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
	return Consent{project: project, suite: suite, proposal: proposal, states: map[consentKey]consentState{}}, nil
}

func (c Consent) Apply(proposal Proposal, governing Policy, command Command) (Consent, CommandResult, error) {
	if command.OperationID() == "" {
		return c, CommandResult{}, ErrInvalidCommand
	}
	if c.project == "" || proposal.IsZero() || governing.ProjectID() != c.project || !c.contains(proposal.Current().Binding().Reference()) || !c.contains(command.Reference()) {
		return c, CommandResult{}, ErrInvalidConsent
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
	} else if !governing.CanRevoke(command.Actor(), command.Actor().ID()) {
		return c.reject(command, ConsentReasonUnauthorized)
	}
	key := consentKey{command.Reference().RevisionID, command.Actor()}
	previous := c.states[key]
	if command.Order() <= previous.order {
		return c.reject(command, ConsentReasonObsoleteCommand)
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
	return next
}

func (c Consent) reject(command Command, reason ConsentReason) (Consent, CommandResult, error) {
	result := CommandResult{command: command, outcome: ConsentRejected, reason: reason}
	return c.record(result), result, nil
}
