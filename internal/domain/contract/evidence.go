package contract

type IntegrityOutcome uint8

const (
	IntegrityPassed IntegrityOutcome = iota + 1
	IntegrityFailed
	IntegrityUnavailable
)

type IntegrityEvidence struct{}

func NewIntegrityEvidence(emitter PrincipalID, source SourceRevision, binding ApprovalBinding, outcome IntegrityOutcome) (IntegrityEvidence, error) {
	return IntegrityEvidence{}, nil
}

func (e IntegrityEvidence) EmitterID() PrincipalID { return "" }

func (e IntegrityEvidence) Source() SourceRevision { return "" }

func (e IntegrityEvidence) Binding() ApprovalBinding { return ApprovalBinding{} }

func (e IntegrityEvidence) Outcome() IntegrityOutcome { return 0 }

func (e IntegrityEvidence) IsZero() bool { return true }
