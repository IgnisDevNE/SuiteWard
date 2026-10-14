package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var _ commands.Store = (*Store)(nil)

// Pending implements commands.Store with one statement.
func (s *Store) Pending(ctx context.Context, horizon time.Time, limit int) ([]commands.Candidate, error) {
	if err := commands.ValidatePending(horizon, limit); err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.pool).PendingCommands(ctx, dbgen.PendingCommandsParams{Horizon: timestamp(horizon), MaxRows: int32(min(limit, math.MaxInt32))})
	if err != nil {
		return nil, fmt.Errorf("read pending commands: %w", err)
	}
	candidates := make([]commands.Candidate, len(rows))
	for i, row := range rows {
		digest, err := artifact.ParseDigest(row.BodyDigest)
		if err != nil {
			return nil, fmt.Errorf("read pending comment %d: stored digest: %w", row.CommentID, err)
		}
		candidates[i] = commands.Candidate{RepositoryID: row.RepositoryID, Repo: row.Repo, PullNumber: int(row.PullNumber), Comment: githubsync.CommentVersion{
			PullNumber: int(row.PullNumber), CommentID: row.CommentID, AuthorID: row.AuthorID, AuthorLogin: row.AuthorLogin, CreatedAt: observedTime(row.CreatedAt),
			UpdatedAt: observedTime(row.UpdatedAt), Body: string(row.Body), BodyDigest: digest, FirstSeen: observedTime(row.FirstSeen), NotSeenSince: observedTime(row.NotSeenSince)}}
	}
	return candidates, nil
}

// Record implements commands.Store in one transaction: the ledger row, then the acknowledgment. A row that already
// exists is compared with the entry once the insert did nothing; a concurrent insert of the same comment waits for
// the first and then does nothing, so the read that follows sees its row.
func (s *Store) Record(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if ack != nil {
		if err := ack.Validate(); err != nil {
			return err
		}
	}
	e = e.AtStoredPrecision()
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin command ledger transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	q := dbgen.New(transaction)
	inserted, err := q.InsertPRCommand(ctx, dbgen.InsertPRCommandParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID, PullNumber: int32(e.PullNumber), // Validate bounds it
		AuthorID: e.AuthorID, CreatedAt: timestamp(e.CreatedAt), BodyDigest: e.BodyDigest.String(), Disposition: string(e.Disposition),
		OperationID: optionalText(string(e.Operation)), DecidedAt: timestamp(e.DecidedAt)})
	if err != nil {
		return fmt.Errorf("write command ledger entry: %w", err)
	}
	if inserted == 0 {
		return sameEntry(ctx, q, e)
	}
	if ack != nil {
		if err := insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, *ack); err != nil {
			return err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit command ledger transaction: %w", err)
	}
	committed = true
	return nil
}

// sameEntry is nil when the stored entry of e's comment is e, and commands.ErrConflict when it is another.
func sameEntry(ctx context.Context, q *dbgen.Queries, e commands.Entry) error {
	row, err := q.GetPRCommand(ctx, dbgen.GetPRCommandParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID})
	if err != nil {
		return fmt.Errorf("read command ledger entry: %w", err)
	}
	if row.PullNumber != int32(e.PullNumber) || row.AuthorID != e.AuthorID || !observedTime(row.CreatedAt).Equal(e.CreatedAt) || row.BodyDigest != e.BodyDigest.String() ||
		row.Disposition != string(e.Disposition) || contract.OperationID(row.OperationID.String) != e.Operation || !observedTime(row.DecidedAt).Equal(e.DecidedAt) {
		return fmt.Errorf("%w: comment %d of repository %d", commands.ErrConflict, e.CommentID, e.RepositoryID)
	}
	return nil
}

// Instance implements commands.Store.
func (s *Store) Instance(ctx context.Context) (string, error) {
	id, err := dbgen.New(s.pool).GetInstanceID(ctx)
	if err != nil {
		return "", fmt.Errorf("read instance id: %w", err)
	}
	return id, nil
}
