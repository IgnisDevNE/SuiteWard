package contract

import "errors"

// StateCheckpoint carries supplied immutable facts for persistence. Restoration
// does not authenticate them or authorize a transition.
type StateCheckpoint struct {
	Canonical CanonicalSnapshot
	Proposal Proposal
	Policy Policy
	Consent Consent
	Scheduling Schedule
	Assessment IntegrityAssessment
	Integration Integration
	History HistoricalCanonical
	PromotionDecision PromotionDecision
	CommandResult CommandResult
	Protected ProtectedContract
	Command Command
}

var ErrInvalidCheckpoint = errors.New("invalid domain checkpoint")

func EncodeStateCheckpoint(StateCheckpoint) ([]byte, error) { return nil, ErrInvalidCheckpoint }
func RestoreStateCheckpoint([]byte) (StateCheckpoint, error) { return StateCheckpoint{}, ErrInvalidCheckpoint }
