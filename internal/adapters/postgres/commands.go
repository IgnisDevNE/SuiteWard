package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	maxRows, err := int32Of(limit)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.pool).PendingCommands(ctx, dbgen.PendingCommandsParams{Horizon: timestamp(horizon), MaxRows: maxRows})
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
	pull, err := int32Of(e.PullNumber)
	if err != nil {
		return err
	}
	return inTx(ctx, s.pool, "command ledger", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		inserted, err := q.InsertPRCommand(ctx, dbgen.InsertPRCommandParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID, PullNumber: pull,
			AuthorID: e.AuthorID, CreatedAt: timestamp(e.CreatedAt), BodyDigest: e.BodyDigest.String(), Disposition: string(e.Disposition),
			OperationID: optionalText(string(e.Operation)), DecidedAt: timestamp(e.DecidedAt)})
		if err != nil {
			return fmt.Errorf("write command ledger entry: %w", err)
		}
		if inserted == 0 {
			return sameEntry(ctx, q, e, pull)
		}
		if ack != nil {
			return insertOutbox(ctx, q, pgtype.Text{}, pgtype.Text{}, *ack)
		}
		return nil
	})
}

// sameEntry is nil when the stored entry of e's comment is e, and commands.ErrConflict when it is another.
func sameEntry(ctx context.Context, q *dbgen.Queries, e commands.Entry, pull int32) error {
	row, err := q.GetPRCommand(ctx, dbgen.GetPRCommandParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID})
	if err != nil {
		return fmt.Errorf("read command ledger entry: %w", err)
	}
	if row.PullNumber != pull || row.AuthorID != e.AuthorID || !observedTime(row.CreatedAt).Equal(e.CreatedAt) || row.BodyDigest != e.BodyDigest.String() ||
		row.Disposition != string(e.Disposition) || contract.OperationID(row.OperationID.String) != e.Operation || !observedTime(row.DecidedAt).Equal(e.DecidedAt) {
		return fmt.Errorf("%w: comment %d of repository %d", commands.ErrConflict, e.CommentID, e.RepositoryID)
	}
	return nil
}

// LatestOwnerCommand implements commands.Store.
func (s *Store) LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID, before int64) (commands.Entry, bool, error) {
	if err := commands.ValidateLatest(repositoryID, pull, ownerID, before); err != nil {
		return commands.Entry{}, false, err
	}
	number, err := int32Of(pull)
	if err != nil {
		return commands.Entry{}, false, err
	}
	row, err := dbgen.New(s.pool).LatestOwnerCommand(ctx, dbgen.LatestOwnerCommandParams{RepositoryID: repositoryID, PullNumber: number, AuthorID: ownerID, Before: before})
	if errors.Is(err, pgx.ErrNoRows) {
		return commands.Entry{}, false, nil
	}
	if err != nil {
		return commands.Entry{}, false, fmt.Errorf("read the latest owner command: %w", err)
	}
	digest, err := artifact.ParseDigest(row.BodyDigest)
	if err != nil {
		return commands.Entry{}, false, fmt.Errorf("read ledger entry %d: stored digest: %w", row.CommentID, err)
	}
	return commands.Entry{RepositoryID: row.RepositoryID, CommentID: row.CommentID, PullNumber: int(row.PullNumber), AuthorID: row.AuthorID, CreatedAt: observedTime(row.CreatedAt),
		BodyDigest: digest, Disposition: commands.Disposition(row.Disposition), Operation: contract.OperationID(row.OperationID.String), DecidedAt: observedTime(row.DecidedAt)}, true, nil
}

// Skipped implements commands.Store.
func (s *Store) Skipped(ctx context.Context, repositoryID int64, pull int, since time.Time) (bool, error) {
	if err := commands.ValidateSkipped(repositoryID, pull); err != nil {
		return false, err
	}
	number, err := int32Of(pull)
	if err != nil {
		return false, err
	}
	skipped, err := dbgen.New(s.pool).PullSkipped(ctx, dbgen.PullSkippedParams{RepositoryID: repositoryID, PullNumber: number, Since: timestamp(since)})
	if err != nil {
		return false, fmt.Errorf("read whether the pull was skipped: %w", err)
	}
	return skipped, nil
}

// PreviewMode implements commands.Store.
func (s *Store) PreviewMode(context.Context, int64, int, time.Time) (string, bool, error) {
	return "", false, nil
}

// Instance implements commands.Store.
func (s *Store) Instance(ctx context.Context) (string, error) {
	id, err := dbgen.New(s.pool).GetInstanceID(ctx)
	if err != nil {
		return "", fmt.Errorf("read instance id: %w", err)
	}
	return id, nil
}
