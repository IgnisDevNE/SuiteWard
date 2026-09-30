package contract

import (
	"errors"
	"strings"
)

// ErrInvalidIntegrityAssessment identifies an incomplete expected context.
var ErrInvalidIntegrityAssessment = errors.New("invalid integrity assessment")

// IntegrityReason distinguishes assessment outcomes without granting authority.
type IntegrityReason uint8

const (
	IntegrityReasonSatisfied IntegrityReason = iota + 1
	IntegrityReasonMissing
	IntegrityReasonMismatch
	IntegrityReasonFailed
	IntegrityReasonUnavailable
)

// AssuranceLevel identifies the guarantee described by an assessment.
type AssuranceLevel uint8

const IntegrityOnly AssuranceLevel = 1

// IntegrityAssessment preserves expected context and the observed evidence.
type IntegrityAssessment struct {
	source    SourceRevision
	binding   ApprovalBinding
	evidence  IntegrityEvidence
	reason    IntegrityReason
	assurance AssuranceLevel
}

// AssessIntegrity compares exact context before interpreting the observation.
// Its result supplies integrity evidence, never consent or promotion authority.
func AssessIntegrity(expectedSource SourceRevision, expectedBinding ApprovalBinding, evidence *IntegrityEvidence) (IntegrityAssessment, error) {
	if strings.TrimSpace(string(expectedSource)) == "" || expectedBinding.IsZero() {
		return IntegrityAssessment{}, ErrInvalidIntegrityAssessment
	}
	assessment := IntegrityAssessment{
		source: expectedSource, binding: expectedBinding,
		reason: IntegrityReasonMissing, assurance: IntegrityOnly,
	}
	if evidence == nil || evidence.IsZero() {
		return assessment, nil
	}
	assessment.evidence = *evidence
	if assessment.evidence.Source() != expectedSource || !assessment.evidence.Binding().Equal(expectedBinding) {
		assessment.reason = IntegrityReasonMismatch
		return assessment, nil
	}
	switch assessment.evidence.Outcome() {
	case IntegrityPassed:
		assessment.reason = IntegrityReasonSatisfied
	case IntegrityFailed:
		assessment.reason = IntegrityReasonFailed
	case IntegrityUnavailable:
		assessment.reason = IntegrityReasonUnavailable
	}
	return assessment, nil
}

func (a IntegrityAssessment) Source() SourceRevision { return a.source }

func (a IntegrityAssessment) Binding() ApprovalBinding { return a.binding }

func (a IntegrityAssessment) Evidence() (IntegrityEvidence, bool) {
	return a.evidence, !a.evidence.IsZero()
}

func (a IntegrityAssessment) Passed() bool { return a.reason == IntegrityReasonSatisfied }

func (a IntegrityAssessment) Reason() IntegrityReason { return a.reason }

func (a IntegrityAssessment) Assurance() AssuranceLevel { return a.assurance }
