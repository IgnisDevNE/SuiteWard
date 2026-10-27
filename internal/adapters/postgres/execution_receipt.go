package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// receiptLedger is execution.ReceiptLedger over the pool of a Store: the check ledger (checks.go) for the Receipt check.
type receiptLedger struct{ pool *pgxpool.Pool }

var _ execution.ReceiptLedger = receiptLedger{}

// ReceiptChecks returns the ledger of the Receipt check of this database.
func (s *Store) ReceiptChecks() execution.ReceiptLedger { return receiptLedger{pool: s.pool} }

func receiptRecord(row dbgen.PrReceiptCheck) execution.ReceiptRecord {
	return execution.ReceiptRecord{ReceiptCheck: execution.ReceiptCheck{RepositoryID: row.RepositoryID, PullNumber: int(row.PullNumber), HeadSHA: row.HeadSha,
		State: execution.CheckState(row.State), Reason: row.Reason, Reference: row.Reference, JobID: row.JobID, Unblocked: row.Unblocked},
		HeadSince: observedTime(row.HeadSince), Generation: row.Generation, DecidedAt: observedTime(row.DecidedAt)}
}

// Current implements execution.ReceiptLedger.
func (l receiptLedger) Current(ctx context.Context, repositoryID int64, number int) (execution.ReceiptRecord, bool, error) {
	if err := execution.ValidatePull(repositoryID, number); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	pull, err := int32Of(number)
	if err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	row, err := dbgen.New(l.pool).GetPRReceiptCheck(ctx, dbgen.GetPRReceiptCheckParams{RepositoryID: repositoryID, PullNumber: pull})
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.ReceiptRecord{}, false, nil
	}
	if err != nil {
		return execution.ReceiptRecord{}, false, fmt.Errorf("read receipt check record: %w", err)
	}
	return receiptRecord(row), true, nil
}

// Publish implements execution.ReceiptLedger in one transaction, as checkLedger.Publish does: the row of the pull is locked first, a pull
// with no row is inserted, and a concurrent first publication waits on the insert and then finds the row.
func (l receiptLedger) Publish(ctx context.Context, r execution.ReceiptCheck, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (execution.ReceiptRecord, bool, error) {
	if at.IsZero() {
		return execution.ReceiptRecord{}, false, fmt.Errorf("%w: the decision time is required", execution.ErrInvalidRequest)
	}
	if err := r.Validate(); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	pull, err := int32Of(r.PullNumber)
	if err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	build := func(generation int64) (governance.OutboxMessage, error) {
		msg, err := message(generation)
		if err != nil {
			return governance.OutboxMessage{}, fmt.Errorf("build the receipt check message: %w", err)
		}
		return msg, msg.Validate()
	}
	var record execution.ReceiptRecord
	var advanced bool
	err = inTx(ctx, l.pool, "receipt check", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		key := dbgen.LockPRReceiptCheckParams{RepositoryID: r.RepositoryID, PullNumber: pull}
		row, err := q.LockPRReceiptCheck(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			var inserted bool
			if record, inserted, err = insertFirstReceipt(ctx, q, build, r, pull, at); err != nil {
				return err
			}
			if inserted {
				advanced = true
				return nil
			}
			row, err = q.LockPRReceiptCheck(ctx, key) // a concurrent publication inserted it first
		}
		if err != nil {
			return fmt.Errorf("lock receipt check record: %w", err)
		}
		failedAt, err := failureTime(ctx, q, row.MessageKey)
		if err != nil {
			return err
		}
		record, advanced, err = advanceReceipt(ctx, q, build, row, r, at, failedAt)
		return err
	})
	if err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	return record, advanced, nil
}

// advanceReceipt stores the next record of a pull that has one, with the new message written first when the result advances the generation.
func advanceReceipt(ctx context.Context, q *dbgen.Queries, build func(int64) (governance.OutboxMessage, error), row dbgen.PrReceiptCheck, r execution.ReceiptCheck,
	at, failedAt time.Time) (execution.ReceiptRecord, bool, error) {
	next, advance := execution.NextReceipt(receiptRecord(row), true, r, at, failedAt)
	messageKey := row.MessageKey
	if advance {
		msg, err := build(next.Generation)
		if err != nil {
			return execution.ReceiptRecord{}, false, err
		}
		if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, msg); err != nil {
			return execution.ReceiptRecord{}, false, err
		}
		messageKey = msg.Key
	}
	err := q.UpdatePRReceiptCheck(ctx, dbgen.UpdatePRReceiptCheckParams{RepositoryID: r.RepositoryID, PullNumber: row.PullNumber, HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince),
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, JobID: next.JobID, Unblocked: next.Unblocked, Generation: next.Generation,
		MessageKey: messageKey, DecidedAt: timestamp(next.DecidedAt)})
	if err != nil {
		return execution.ReceiptRecord{}, false, fmt.Errorf("update receipt check record: %w", err)
	}
	return next, advance, nil
}

// insertFirstReceipt writes the first record of a pull and its message. It reports inserted false, and writes nothing, when the pull has a
// row after all: a concurrent first publication committed it while this insert waited.
func insertFirstReceipt(ctx context.Context, q *dbgen.Queries, build func(int64) (governance.OutboxMessage, error), r execution.ReceiptCheck, pull int32, at time.Time) (execution.ReceiptRecord, bool, error) {
	next, _ := execution.NextReceipt(execution.ReceiptRecord{}, false, r, at, time.Time{})
	msg, err := build(next.Generation)
	if err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	inserted, err := q.InsertPRReceiptCheck(ctx, dbgen.InsertPRReceiptCheckParams{RepositoryID: r.RepositoryID, PullNumber: pull, HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince),
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, JobID: next.JobID, Unblocked: next.Unblocked, Generation: next.Generation,
		MessageKey: msg.Key, DecidedAt: timestamp(next.DecidedAt)})
	if err != nil {
		return execution.ReceiptRecord{}, false, fmt.Errorf("write receipt check record: %w", err)
	}
	if inserted == 0 {
		return execution.ReceiptRecord{}, false, nil
	}
	if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, msg); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	return next, true, nil
}
