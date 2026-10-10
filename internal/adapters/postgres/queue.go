package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Delivery states of an outbox message.
const (
	OutboxPending   = "pending"
	OutboxDelivered = "delivered"
	OutboxFailed    = "failed"
)

// SystemStatus is the state of a system job and its outbox message: River's
// job state and the outbox state, as text.
type SystemStatus struct {
	JobState    string
	OutboxState string
}

// ClaimOutboxParams selects the due outbox messages a relay claims.
type ClaimOutboxParams struct {
	Now         time.Time     // messages whose next attempt is due at Now
	Lease       time.Duration // how long a claim hides the message from other relays
	MaxAttempts int           // pending messages that used all attempts are failed instead
	Limit       int
}

// OutboxClaim is one claimed message. Attempts counts this delivery.
type OutboxClaim struct {
	Key      string
	Kind     string
	Payload  json.RawMessage
	Attempts int
}

// OutboxUpdate is the outcome of one delivery attempt: State pending with the
// time of the next attempt, or a terminal state with its finish time.
type OutboxUpdate struct {
	State         string
	LastError     string
	NextAttemptAt time.Time // State pending only
	FinishedAt    time.Time // terminal states only
}

// EnqueueSystem inserts a job and an outbox message with no Suite in one
// transaction, using the statements of Tx.Enqueue and Tx.Outbox. It returns
// the job id.
func (s *Store) EnqueueSystem(ctx context.Context, job governance.Job, message governance.OutboxMessage) (int64, error) {
	return 0, errors.New("not implemented")
}

// SystemStatus reports the state of a job and an outbox message written by
// EnqueueSystem; either unknown is governance.ErrNotFound.
func (s *Store) SystemStatus(ctx context.Context, jobID int64, outboxKey string) (SystemStatus, error) {
	return SystemStatus{}, errors.New("not implemented")
}

// ClaimOutbox claims due pending messages with FOR UPDATE SKIP LOCKED: it
// counts the attempt and hides the message until the lease expires.
func (s *Store) ClaimOutbox(ctx context.Context, params ClaimOutboxParams) ([]OutboxClaim, error) {
	return nil, errors.New("not implemented")
}

// FinishOutbox records the outcome of a claim. It updates nothing, and
// reports false, when another relay has reclaimed the message since.
func (s *Store) FinishOutbox(ctx context.Context, claim OutboxClaim, update OutboxUpdate) (bool, error) {
	return false, errors.New("not implemented")
}
