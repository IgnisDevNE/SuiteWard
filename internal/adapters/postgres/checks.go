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
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// checkLedger is checks.Store over the pool of a Store. It is a separate type because Store already has the
// method Current, of owner.Store.
type checkLedger struct{ pool *pgxpool.Pool }

var _ checks.Store = checkLedger{}

// Checks returns the PR check ledger of this database.
func (s *Store) Checks() checks.Store { return checkLedger{pool: s.pool} }

func checkRecord(row dbgen.PrCheck) checks.Record {
	return checks.Record{Result: checks.Result{RepositoryID: row.RepositoryID, PullNumber: int(row.PullNumber), HeadSHA: row.HeadSha, State: checks.State(row.State),
		Reason: row.Reason, Reference: row.Reference, Change: row.Change, Revision: row.Revision, CommandID: row.CommandID},
		HeadSince: observedTime(row.HeadSince), Generation: row.Generation, DecidedAt: observedTime(row.DecidedAt)}
}

// Current implements checks.Store.
func (l checkLedger) Current(ctx context.Context, repositoryID int64, number int) (checks.Record, bool, error) {
	if err := checks.ValidatePull(repositoryID, number); err != nil {
		return checks.Record{}, false, err
	}
	row, err := dbgen.New(l.pool).GetPRCheck(ctx, dbgen.GetPRCheckParams{RepositoryID: repositoryID, PullNumber: int32(number)}) // ValidatePull bounds it
	if errors.Is(err, pgx.ErrNoRows) {
		return checks.Record{}, false, nil
	}
	if err != nil {
		return checks.Record{}, false, fmt.Errorf("read check record: %w", err)
	}
	return checkRecord(row), true, nil
}

// Publish implements checks.Store in one transaction. The row of the pull is locked first; a pull with no row
// is inserted, and a concurrent first publication waits on the insert and then finds the row.
func (l checkLedger) Publish(ctx context.Context, r checks.Result, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
	if err := checks.ValidatePublish(r, at); err != nil {
		return checks.Record{}, false, err
	}
	transaction, err := l.pool.Begin(ctx)
	if err != nil {
		return checks.Record{}, false, fmt.Errorf("begin check transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	q := dbgen.New(transaction)
	build := func(generation int64) (governance.OutboxMessage, error) {
		msg, err := message(generation)
		if err != nil {
			return governance.OutboxMessage{}, fmt.Errorf("build the check message: %w", err)
		}
		return msg, msg.Validate()
	}
	key := dbgen.LockPRCheckParams{RepositoryID: r.RepositoryID, PullNumber: int32(r.PullNumber)} // ValidatePublish bounds it
	row, err := q.LockPRCheck(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		var first checks.Record
		if first, err = insertFirstCheck(ctx, q, build, r, at); err != nil {
			return checks.Record{}, false, err
		}
		if first.Generation > 0 {
			if err := transaction.Commit(ctx); err != nil {
				return checks.Record{}, false, translateWrite("commit check transaction", err)
			}
			committed = true
			return first, true, nil
		}
		row, err = q.LockPRCheck(ctx, key) // a concurrent publication inserted it first
	}
	if err != nil {
		return checks.Record{}, false, fmt.Errorf("lock check record: %w", err)
	}
	outcome, err := q.GetOutboxOutcome(ctx, row.MessageKey)
	if err != nil {
		return checks.Record{}, false, fmt.Errorf("read the outbox state of check message %q: %w", row.MessageKey, err)
	}
	var failedAt time.Time // zero: the current message did not fail
	if outcome.State == OutboxFailed {
		failedAt = observedTime(outcome.FinishedAt) // the outbox constraint gives every ended message a finished_at
	}
	next, advance := checks.Next(checkRecord(row), true, r, at, failedAt)
	messageKey := row.MessageKey
	if advance {
		msg, err := build(next.Generation)
		if err != nil {
			return checks.Record{}, false, err
		}
		if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, msg); err != nil {
			return checks.Record{}, false, err
		}
		messageKey = msg.Key
	}
	if err := q.UpdatePRCheck(ctx, dbgen.UpdatePRCheckParams{RepositoryID: r.RepositoryID, PullNumber: key.PullNumber, HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince),
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, Change: next.Change, Revision: next.Revision, CommandID: next.CommandID,
		Generation: next.Generation, MessageKey: messageKey, DecidedAt: timestamp(next.DecidedAt)}); err != nil {
		return checks.Record{}, false, fmt.Errorf("update check record: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return checks.Record{}, false, translateWrite("commit check transaction", err)
	}
	committed = true
	return next, advance, nil
}

// insertFirstCheck writes the first record of a pull and its message. It returns the zero record when the pull
// has a row after all: a concurrent first publication committed it while this insert waited.
func insertFirstCheck(ctx context.Context, q *dbgen.Queries, build func(int64) (governance.OutboxMessage, error), r checks.Result, at time.Time) (checks.Record, error) {
	next, _ := checks.Next(checks.Record{}, false, r, at, time.Time{})
	msg, err := build(next.Generation)
	if err != nil {
		return checks.Record{}, err
	}
	inserted, err := q.InsertPRCheck(ctx, dbgen.InsertPRCheckParams{RepositoryID: r.RepositoryID, PullNumber: int32(r.PullNumber), HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince), // ValidatePublish bounds the pull
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, Change: next.Change, Revision: next.Revision, CommandID: next.CommandID,
		Generation: next.Generation, MessageKey: msg.Key, DecidedAt: timestamp(next.DecidedAt)})
	if err != nil {
		return checks.Record{}, fmt.Errorf("write check record: %w", err)
	}
	if inserted == 0 {
		return checks.Record{}, nil
	}
	if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, msg); err != nil {
		return checks.Record{}, err
	}
	return next, nil
}
