package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var _ githubsync.Store = (*Store)(nil)

// pullRow and commentRow are the elements of the JSON arrays the batch
// statements of queries/github.sql expand, so one statement writes a whole
// listing.
type pullRow struct {
	Number    int       `json:"number"`
	HeadSHA   string    `json:"head_sha"`
	HeadRef   string    `json:"head_ref"`
	BaseRef   string    `json:"base_ref"`
	BaseSHA   string    `json:"base_sha"`
	UpdatedAt time.Time `json:"updated_at"`
}

type commentRow struct {
	CommentID   int64     `json:"comment_id"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullNumber  int       `json:"pull_number"`
	AuthorID    int64     `json:"author_id"`
	AuthorLogin string    `json:"author_login"`
	CreatedAt   time.Time `json:"created_at"`
	Body        string    `json:"body"`
	BodyDigest  string    `json:"body_digest"`
}

// observedTime is a stored observation time: NULL is the zero time.
func observedTime(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// Repository implements githubsync.Store.
func (s *Store) Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error) {
	row, err := dbgen.New(s.pool).GetGitHubRepository(ctx, repo)
	if errors.Is(err, pgx.ErrNoRows) {
		return githubsync.RepositoryState{}, false, nil
	}
	if err != nil {
		return githubsync.RepositoryState{}, false, fmt.Errorf("read GitHub repository: %w", err)
	}
	return githubsync.RepositoryState{
		Repo: row.Repo, InstallationID: row.InstallationID, LastSuccess: observedTime(row.LastSuccess),
		LastError: githubsync.FailureKind(row.LastError), LastStatus: int(row.LastStatus), WaitUntil: observedTime(row.WaitUntil),
	}, true, nil
}

// RecordCycle implements githubsync.Store in one transaction. Its first
// statement upserts, and so locks, the repository row until the commit: a
// concurrent cycle or failure of the same repository waits for it and then
// computes its marks from what this one committed.
func (s *Store) RecordCycle(ctx context.Context, cycle githubsync.Cycle) error {
	if err := cycle.Validate(); err != nil {
		return err
	}
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin GitHub cycle transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	if err := recordCycle(ctx, dbgen.New(transaction), cycle); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit GitHub cycle transaction: %w", err)
	}
	committed = true
	return nil
}

func recordCycle(ctx context.Context, q *dbgen.Queries, cycle githubsync.Cycle) error {
	at := timestamp(cycle.At)
	if err := q.UpsertRepositorySuccess(ctx, dbgen.UpsertRepositorySuccessParams{Repo: cycle.Repo, InstallationID: cycle.InstallationID, At: at}); err != nil {
		return fmt.Errorf("write GitHub repository: %w", err)
	}

	pulls, open := make([]pullRow, 0, len(cycle.OpenPulls)), make([]int32, 0, len(cycle.OpenPulls))
	for _, pull := range cycle.OpenPulls {
		pulls = append(pulls, pullRow{Number: pull.Number, HeadSHA: pull.HeadSHA, HeadRef: pull.HeadRef, BaseRef: pull.BaseRef, BaseSHA: pull.BaseSHA, UpdatedAt: pull.UpdatedAt})
		open = append(open, int32(pull.Number)) // Validate bounds the number
	}
	pullsJSON, err := json.Marshal(pulls)
	if err != nil {
		return fmt.Errorf("encode GitHub pulls: %w", err)
	}
	if err := q.UpsertPulls(ctx, dbgen.UpsertPullsParams{Repo: cycle.Repo, At: at, Pulls: pullsJSON}); err != nil {
		return fmt.Errorf("write GitHub pulls: %w", err)
	}
	if err := q.MarkPullsNotOpen(ctx, dbgen.MarkPullsNotOpenParams{Repo: cycle.Repo, At: at, OpenNumbers: open}); err != nil {
		return fmt.Errorf("mark GitHub pulls not open: %w", err)
	}

	comments, seen := []commentRow{}, []int64{}
	for number, listing := range cycle.Comments {
		for _, comment := range listing {
			comments = append(comments, commentRow{CommentID: comment.CommentID, UpdatedAt: comment.UpdatedAt, PullNumber: number, AuthorID: comment.AuthorID,
				AuthorLogin: comment.AuthorLogin, CreatedAt: comment.CreatedAt, Body: comment.Body, BodyDigest: artifact.Hash([]byte(comment.Body)).String()})
			seen = append(seen, comment.CommentID)
		}
	}
	commentsJSON, err := json.Marshal(comments)
	if err != nil {
		return fmt.Errorf("encode GitHub comments: %w", err)
	}
	changed, err := q.CommentsWithChangedCreation(ctx, dbgen.CommentsWithChangedCreationParams{Repo: cycle.Repo, Comments: commentsJSON})
	if err != nil {
		return fmt.Errorf("check GitHub comment creation times: %w", err)
	}
	if len(changed) > 0 {
		return fmt.Errorf("GitHub comment %d changed its creation time", changed[0])
	}
	if err := q.ClearCommentsNotSeen(ctx, dbgen.ClearCommentsNotSeenParams{Repo: cycle.Repo, CommentIds: seen}); err != nil {
		return fmt.Errorf("clear GitHub comment absence: %w", err)
	}
	if err := q.InsertCommentVersions(ctx, dbgen.InsertCommentVersionsParams{Repo: cycle.Repo, At: at, Comments: commentsJSON}); err != nil {
		return fmt.Errorf("write GitHub comments: %w", err)
	}
	if err := q.MarkCommentsNotSeen(ctx, dbgen.MarkCommentsNotSeenParams{Repo: cycle.Repo, At: at, ListedPulls: open, ListedComments: seen}); err != nil {
		return fmt.Errorf("mark GitHub comments not seen: %w", err)
	}
	return nil
}

// RecordFailure implements githubsync.Store. One upsert is one transaction,
// and it locks the repository row like a cycle does.
func (s *Store) RecordFailure(ctx context.Context, failure githubsync.Failure) error {
	if err := failure.Validate(); err != nil {
		return err
	}
	err := dbgen.New(s.pool).UpsertRepositoryFailure(ctx, dbgen.UpsertRepositoryFailureParams{
		Repo: failure.Repo, InstallationID: failure.InstallationID, LastError: string(failure.Kind), LastStatus: int32(failure.Status), WaitUntil: timestamp(failure.WaitUntil),
	})
	if err != nil {
		return fmt.Errorf("write GitHub failure: %w", err)
	}
	return nil
}

// Pulls implements githubsync.Store.
func (s *Store) Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error) {
	rows, err := dbgen.New(s.pool).ListGitHubPulls(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("read GitHub pulls: %w", err)
	}
	pulls := make([]githubsync.PullState, len(rows))
	for i, row := range rows {
		pulls[i] = githubsync.PullState{Number: int(row.Number), HeadSHA: row.HeadSha, HeadRef: row.HeadRef, BaseRef: row.BaseRef, BaseSHA: row.BaseSha,
			UpdatedAt: observedTime(row.UpdatedAt), FirstSeen: observedTime(row.FirstSeen), LastSeen: observedTime(row.LastSeen), NotOpenSince: observedTime(row.NotOpenSince)}
	}
	return pulls, nil
}

// Comments implements githubsync.Store.
func (s *Store) Comments(ctx context.Context, repo string, number int) ([]githubsync.CommentVersion, error) {
	if number < 1 || number > math.MaxInt32 {
		return nil, fmt.Errorf("invalid GitHub pull number %d", number)
	}
	rows, err := dbgen.New(s.pool).ListGitHubComments(ctx, dbgen.ListGitHubCommentsParams{Repo: repo, PullNumber: int32(number)})
	if err != nil {
		return nil, fmt.Errorf("read GitHub comments: %w", err)
	}
	versions := make([]githubsync.CommentVersion, len(rows))
	for i, row := range rows {
		digest, err := artifact.ParseDigest(row.BodyDigest)
		if err != nil {
			return nil, fmt.Errorf("read GitHub comment %d: stored digest: %w", row.CommentID, err)
		}
		versions[i] = githubsync.CommentVersion{PullNumber: int(row.PullNumber), CommentID: row.CommentID, AuthorID: row.AuthorID, AuthorLogin: row.AuthorLogin,
			CreatedAt: observedTime(row.CreatedAt), UpdatedAt: observedTime(row.UpdatedAt), Body: row.Body, BodyDigest: digest,
			FirstSeen: observedTime(row.FirstSeen), NotSeenSince: observedTime(row.NotSeenSince)}
	}
	return versions, nil
}
