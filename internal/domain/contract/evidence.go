package contract

// IntegrityOutcome describes an integrity observation, not test execution.
type IntegrityOutcome uint8

const (
	IntegrityPassed IntegrityOutcome = iota + 1
	IntegrityFailed
	IntegrityUnavailable
)

// IntegrityEvidence is immutable attribution and context, not authentication.
type IntegrityEvidence struct {
	emitter PrincipalID
	source  SourceRevision
	binding ApprovalBinding
	outcome IntegrityOutcome
}

func NewIntegrityEvidence(emitter PrincipalID, source SourceRevision, binding ApprovalBinding, outcome IntegrityOutcome) (IntegrityEvidence, error) {
	return IntegrityEvidence{emitter: emitter, source: source, binding: binding, outcome: outcome}, nil
}

func (e IntegrityEvidence) EmitterID() PrincipalID { return e.emitter }

func (e IntegrityEvidence) Source() SourceRevision { return e.source }

func (e IntegrityEvidence) Binding() ApprovalBinding { return e.binding }

func (e IntegrityEvidence) Outcome() IntegrityOutcome { return e.outcome }

func (e IntegrityEvidence) IsZero() bool { return e.binding.IsZero() }
