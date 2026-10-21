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
	pull, err := int32Of(number)
	if err != nil {
		return checks.Record{}, false, err
	}
	row, err := dbgen.New(l.pool).GetPRCheck(ctx, dbgen.GetPRCheckParams{RepositoryID: repositoryID, PullNumber: pull})
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
	pull, err := int32Of(r.PullNumber)
	if err != nil {
		return checks.Record{}, false, err
	}
	build := func(generation int64) (governance.OutboxMessage, error) {
		msg, err := message(generation)
		if err != nil {
			return governance.OutboxMessage{}, fmt.Errorf("build the check message: %w", err)
		}
		return msg, msg.Validate()
	}
	var record checks.Record
	var advanced bool
	err = inTx(ctx, l.pool, "check", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		key := dbgen.LockPRCheckParams{RepositoryID: r.RepositoryID, PullNumber: pull}
		row, err := q.LockPRCheck(ctx, key)
		if errors.Is(err, pgx.ErrNoRows) {
			var inserted bool
			if record, inserted, err = insertFirstCheck(ctx, q, build, r, at); err != nil {
				return err
			}
			if inserted {
				advanced = true
				return nil
			}
			row, err = q.LockPRCheck(ctx, key) // a concurrent publication inserted it first
		}
		if err != nil {
			return fmt.Errorf("lock check record: %w", err)
		}
		failedAt, err := failureTime(ctx, q, row.MessageKey)
		if err != nil {
			return err
		}
		record, advanced, err = advanceCheck(ctx, q, row, r, at, failedAt, build)
		return err
	})
	if err != nil {
		return checks.Record{}, false, err
	}
	return record, advanced, nil
}

// failureTime is when the current message of a check failed; zero when it did not.
func failureTime(ctx context.Context, q *dbgen.Queries, messageKey string) (time.Time, error) {
	outcome, err := q.GetOutboxOutcome(ctx, messageKey)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the outbox state of check message %q: %w", messageKey, err)
	}
	if outcome.State != OutboxFailed {
		return time.Time{}, nil
	}
	if !outcome.FinishedAt.Valid { // the outbox constraint forbids it; a zero time would read as "not failed" and never retry
		return time.Time{}, fmt.Errorf("check message %q ended failed without a finish time", messageKey)
	}
	return outcome.FinishedAt.Time.UTC(), nil
}

// advanceCheck stores the next record of a pull that has one. When the result advances the generation, the new
// message is written first.
func advanceCheck(ctx context.Context, q *dbgen.Queries, row dbgen.PrCheck, r checks.Result, at, failedAt time.Time, build func(int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
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
	if err := q.UpdatePRCheck(ctx, dbgen.UpdatePRCheckParams{RepositoryID: r.RepositoryID, PullNumber: row.PullNumber, HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince),
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, Change: next.Change, Revision: next.Revision, CommandID: next.CommandID,
		Generation: next.Generation, MessageKey: messageKey, DecidedAt: timestamp(next.DecidedAt)}); err != nil {
		return checks.Record{}, false, fmt.Errorf("update check record: %w", err)
	}
	return next, advance, nil
}

// insertFirstCheck writes the first record of a pull and its message. It reports inserted false, and writes
// nothing, when the pull has a row after all: a concurrent first publication committed it while this insert waited.
func insertFirstCheck(ctx context.Context, q *dbgen.Queries, build func(int64) (governance.OutboxMessage, error), r checks.Result, at time.Time) (checks.Record, bool, error) {
	next, _ := checks.Next(checks.Record{}, false, r, at, time.Time{})
	msg, err := build(next.Generation)
	if err != nil {
		return checks.Record{}, false, err
	}
	pull, err := int32Of(r.PullNumber)
	if err != nil {
		return checks.Record{}, false, err
	}
	inserted, err := q.InsertPRCheck(ctx, dbgen.InsertPRCheckParams{RepositoryID: r.RepositoryID, PullNumber: pull, HeadSha: next.HeadSHA, HeadSince: timestamp(next.HeadSince),
		State: string(next.State), Reason: next.Reason, Reference: next.Reference, Change: next.Change, Revision: next.Revision, CommandID: next.CommandID,
		Generation: next.Generation, MessageKey: msg.Key, DecidedAt: timestamp(next.DecidedAt)})
	if err != nil {
		return checks.Record{}, false, fmt.Errorf("write check record: %w", err)
	}
	if inserted == 0 {
		return checks.Record{}, false, nil
	}
	if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, msg); err != nil {
		return checks.Record{}, false, err
	}
	return next, true, nil
}
