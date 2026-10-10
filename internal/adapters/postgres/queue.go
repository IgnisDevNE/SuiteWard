package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
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

// OutboxRef names an outbox message without its payload.
type OutboxRef struct {
	Key  string
	Kind string
}

// ClaimOutboxResult is what one claim pass found: the claimed messages and
// the pending messages it failed because every attempt was used up.
type ClaimOutboxResult struct {
	Claims    []OutboxClaim
	Abandoned []OutboxRef
}

// OutboxUpdate is the outcome of one delivery attempt: State pending with the
// time of the next attempt, or a terminal state with its finish time.
type OutboxUpdate struct {
	State         string
	LastError     string    // empty keeps the earlier error
	NextAttemptAt time.Time // State pending only
	FinishedAt    time.Time // terminal states only
}

func timestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

// enqueue validates job and inserts it through tx.
func enqueue(ctx context.Context, jobs JobInserter, tx pgx.Tx, job governance.Job) (int64, error) {
	if err := job.Validate(); err != nil {
		return 0, err
	}
	id, err := jobs.Insert(ctx, tx, job)
	if err != nil {
		return 0, fmt.Errorf("enqueue job: %w", err)
	}
	return id, nil
}

// insertOutbox validates message and inserts it with the given scope, which is
// null for a system message.
func insertOutbox(ctx context.Context, q *dbgen.Queries, project, suite pgtype.Text, message governance.OutboxMessage) error {
	if err := message.Validate(); err != nil {
		return err
	}
	err := q.InsertOutbox(ctx, dbgen.InsertOutboxParams{Key: message.Key, ProjectID: project, SuiteID: suite, Kind: message.Kind, Payload: message.Payload})
	if err != nil {
		return translateWrite("write outbox message", err)
	}
	return nil
}

// EnqueueSystem inserts a job and an outbox message with no Suite in one
// transaction, using the statements of Tx.Enqueue and Tx.Outbox. It returns
// the job id.
func (s *Store) EnqueueSystem(ctx context.Context, job governance.Job, message governance.OutboxMessage) (int64, error) {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin system transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	id, err := enqueue(ctx, s.jobs, transaction, job)
	if err != nil {
		return 0, err
	}
	if err := insertOutbox(ctx, dbgen.New(transaction), pgtype.Text{}, pgtype.Text{}, message); err != nil {
		return 0, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit system transaction: %w", err)
	}
	committed = true
	return id, nil
}

// SystemStatus reports the state of a job and an outbox message written by
// EnqueueSystem; either unknown is governance.ErrNotFound.
func (s *Store) SystemStatus(ctx context.Context, jobID int64, outboxKey string) (SystemStatus, error) {
	q := dbgen.New(s.pool)
	job, err := q.GetJobState(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SystemStatus{}, fmt.Errorf("%w: job %d", governance.ErrNotFound, jobID)
	}
	if err != nil {
		return SystemStatus{}, fmt.Errorf("read job state: %w", err)
	}
	message, err := q.GetOutboxState(ctx, outboxKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return SystemStatus{}, fmt.Errorf("%w: outbox message %q", governance.ErrNotFound, outboxKey)
	}
	if err != nil {
		return SystemStatus{}, fmt.Errorf("read outbox state: %w", err)
	}
	return SystemStatus{JobState: job, OutboxState: message}, nil
}

// ClaimOutbox claims due pending messages with FOR UPDATE SKIP LOCKED: it
// counts the attempt and hides the message until the lease expires. Pending
// messages that used every attempt are failed instead of claimed.
func (s *Store) ClaimOutbox(ctx context.Context, params ClaimOutboxParams) (ClaimOutboxResult, error) {
	q := dbgen.New(s.pool)
	if err := q.FailExhaustedOutbox(ctx, dbgen.FailExhaustedOutboxParams{Now: timestamp(params.Now), MaxAttempts: int32(params.MaxAttempts)}); err != nil {
		return ClaimOutboxResult{}, fmt.Errorf("fail exhausted outbox messages: %w", err)
	}
	rows, err := q.ClaimOutbox(ctx, dbgen.ClaimOutboxParams{Now: timestamp(params.Now), LeaseUntil: timestamp(params.Now.Add(params.Lease)), MaxAttempts: int32(params.MaxAttempts), Batch: int32(params.Limit)})
	if err != nil {
		return ClaimOutboxResult{}, fmt.Errorf("claim outbox messages: %w", err)
	}
	claims := make([]OutboxClaim, len(rows))
	for i, row := range rows {
		claims[i] = OutboxClaim{Key: row.Key, Kind: row.Kind, Payload: row.Payload, Attempts: int(row.Attempts)}
	}
	return ClaimOutboxResult{Claims: claims}, nil
}

// FinishOutbox records the outcome of a claim. It updates nothing, and
// reports false, when another relay has reclaimed the message since.
func (s *Store) FinishOutbox(ctx context.Context, claim OutboxClaim, update OutboxUpdate) (bool, error) {
	updated, err := dbgen.New(s.pool).FinishOutbox(ctx, dbgen.FinishOutboxParams{
		State: update.State, LastError: optionalText(update.LastError), NextAttemptAt: timestamp(update.NextAttemptAt),
		FinishedAt: timestamp(update.FinishedAt), Key: claim.Key, Attempts: int32(claim.Attempts),
	})
	if err != nil {
		return false, fmt.Errorf("record outbox outcome: %w", err)
	}
	return updated == 1, nil
}

// ReleaseOutbox gives back a claim that was never tried: it restores the
// attempt and makes the message due at nextAttemptAt. It updates nothing, and
// reports false, when another relay has reclaimed the message since.
func (s *Store) ReleaseOutbox(ctx context.Context, claim OutboxClaim, nextAttemptAt time.Time) (bool, error) {
	return false, errors.New("not implemented")
}
