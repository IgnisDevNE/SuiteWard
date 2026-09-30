package contract

type IntegrityReason uint8

const (
	IntegrityReasonSatisfied IntegrityReason = iota + 1
	IntegrityReasonMissing
	IntegrityReasonMismatch
	IntegrityReasonFailed
	IntegrityReasonUnavailable
)

type AssuranceLevel uint8

const IntegrityOnly AssuranceLevel = 1

type IntegrityAssessment struct{}

func AssessIntegrity(expectedSource SourceRevision, expectedBinding ApprovalBinding, evidence *IntegrityEvidence) (IntegrityAssessment, error) {
	return IntegrityAssessment{}, nil
}

func (a IntegrityAssessment) Source() SourceRevision { return "" }

func (a IntegrityAssessment) Binding() ApprovalBinding { return ApprovalBinding{} }

func (a IntegrityAssessment) Evidence() (IntegrityEvidence, bool) { return IntegrityEvidence{}, false }

func (a IntegrityAssessment) Passed() bool { return false }

func (a IntegrityAssessment) Reason() IntegrityReason { return 0 }

func (a IntegrityAssessment) Assurance() AssuranceLevel { return 0 }
