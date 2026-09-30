package contract

import (
	"errors"
	"strings"
)

// ErrInvalidIntegrityEvidence identifies an incomplete or invalid observation.
var ErrInvalidIntegrityEvidence = errors.New("invalid integrity evidence")

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

// NewIntegrityEvidence records an observation whose provenance the caller has
// authenticated. The emitter ID alone does not establish trust or authority.
func NewIntegrityEvidence(emitter PrincipalID, source SourceRevision, binding ApprovalBinding, outcome IntegrityOutcome) (IntegrityEvidence, error) {
	if strings.TrimSpace(string(emitter)) == "" || strings.TrimSpace(string(source)) == "" || binding.IsZero() {
		return IntegrityEvidence{}, ErrInvalidIntegrityEvidence
	}
	if outcome != IntegrityPassed && outcome != IntegrityFailed && outcome != IntegrityUnavailable {
		return IntegrityEvidence{}, ErrInvalidIntegrityEvidence
	}
	return IntegrityEvidence{emitter: emitter, source: source, binding: binding, outcome: outcome}, nil
}

func (e IntegrityEvidence) EmitterID() PrincipalID { return e.emitter }

func (e IntegrityEvidence) Source() SourceRevision { return e.source }

func (e IntegrityEvidence) Binding() ApprovalBinding { return e.binding }

func (e IntegrityEvidence) Outcome() IntegrityOutcome { return e.outcome }

func (e IntegrityEvidence) IsZero() bool { return e.binding.IsZero() }
