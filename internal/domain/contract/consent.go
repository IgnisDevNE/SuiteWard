package contract

import "errors"

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
type Consent struct{}

func NewConsent(project ProjectID, suite SuiteID, proposal ProposalID) (Consent, error) {
	return Consent{}, nil
}

func (c Consent) Apply(proposal Proposal, governing Policy, command Command) (Consent, CommandResult, error) {
	return c, CommandResult{}, nil
}

// HasApproval reports consent eligibility only; it never authorizes promotion.
func (c Consent) HasApproval(proposal Proposal, governing Policy) bool { return false }

func (c Consent) Results() []CommandResult { return nil }
