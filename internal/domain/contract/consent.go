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
	result := CommandResult{command: command, outcome: ConsentApproved}
	next := c.record(result)
	next.states = maps.Clone(c.states)
	next.states[consentKey{command.Reference().RevisionID, command.Actor()}] = consentState{binding: proposal.Current().Binding(), active: true}
	return next, result, nil
}

// HasApproval reports consent eligibility only; it never authorizes promotion.
func (c Consent) HasApproval(proposal Proposal, governing Policy) bool { return len(c.states) > 0 }

func (c Consent) Results() []CommandResult { return slices.Clone(c.results) }

func (c Consent) contains(reference ProposalReference) bool {
	return c.project == reference.ProjectID && c.suite == reference.SuiteID && c.proposal == reference.ProposalID
}

func (c Consent) record(result CommandResult) Consent {
	next := c
	next.results = append(slices.Clone(c.results), result)
	return next
}
