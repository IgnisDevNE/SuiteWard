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
	HeadRepo  string    `json:"head_repo"` // "" is NULL: not observed
}

type commentRow struct {
	CommentID   int64     `json:"comment_id"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullNumber  int       `json:"pull_number"`
	AuthorID    int64     `json:"author_id"`
	AuthorLogin string    `json:"author_login"`
	CreatedAt   time.Time `json:"created_at"`
	Body        []byte    `json:"body"` // encoding/json writes bytes as base64; the SQL decodes it
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
		Repo: row.Repo, InstallationID: row.InstallationID, LastSuccess: observedTime(row.LastSuccess), CycleStartedAt: observedTime(row.CycleStartedAt),
		LastError: githubsync.FailureKind(row.LastError), LastStatus: int(row.LastStatus), WaitUntil: observedTime(row.WaitUntil),
	}, true, nil
}

// RecordCycle implements githubsync.Store in one transaction. Its first
// statement upserts, and so locks, the repository row until the commit: a
// concurrent cycle or failure of the same repository waits for it and then
// computes its marks from what this one committed.
func (s *Store) RecordCycle(ctx context.Context, cycle githubsync.Cycle) ([]int64, error) {
	if err := cycle.Validate(); err != nil {
		return nil, err
	}
	var ids []int64
	err := inTx(ctx, s.pool, "GitHub cycle", func(transaction pgx.Tx) (err error) {
		ids, err = recordCycle(ctx, dbgen.New(transaction), cycle)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// recordCycle writes the cycle and returns the ids of the comments the store had never seen.
func recordCycle(ctx context.Context, q *dbgen.Queries, cycle githubsync.Cycle) ([]int64, error) {
	at := timestamp(cycle.At)
	if err := q.UpsertRepositorySuccess(ctx, dbgen.UpsertRepositorySuccessParams{Repo: cycle.Repo, InstallationID: cycle.InstallationID, At: at, StartedAt: timestamp(cycle.StartedAt)}); err != nil {
		return nil, fmt.Errorf("write GitHub repository: %w", err)
	}

	pulls, open := make([]pullRow, 0, len(cycle.OpenPulls)), make([]int32, 0, len(cycle.OpenPulls))
	for _, pull := range cycle.OpenPulls {
		pulls = append(pulls, pullRow{Number: pull.Number, HeadSHA: pull.HeadSHA, HeadRef: pull.HeadRef, BaseRef: pull.BaseRef, BaseSHA: pull.BaseSHA, UpdatedAt: pull.UpdatedAt, HeadRepo: pull.HeadRepo})
		number, err := int32Of(pull.Number)
		if err != nil {
			return nil, err
		}
		open = append(open, number)
	}
	pullsJSON, err := json.Marshal(pulls)
	if err != nil {
		return nil, fmt.Errorf("encode GitHub pulls: %w", err)
	}
	if err := q.UpsertPulls(ctx, dbgen.UpsertPullsParams{Repo: cycle.Repo, At: at, Pulls: pullsJSON}); err != nil {
		return nil, fmt.Errorf("write GitHub pulls: %w", err)
	}
	if err := q.MarkPullsNotOpen(ctx, dbgen.MarkPullsNotOpenParams{Repo: cycle.Repo, At: at, OpenNumbers: open}); err != nil {
		return nil, fmt.Errorf("mark GitHub pulls not open: %w", err)
	}

	comments, seen := []commentRow{}, []int64{}
	for number, listing := range cycle.Comments {
		for _, comment := range listing {
			comments = append(comments, commentRow{CommentID: comment.CommentID, UpdatedAt: comment.UpdatedAt, PullNumber: number, AuthorID: comment.AuthorID,
				AuthorLogin: comment.AuthorLogin, CreatedAt: comment.CreatedAt, Body: []byte(comment.Body), BodyDigest: artifact.Hash([]byte(comment.Body)).String()})
			seen = append(seen, comment.CommentID)
		}
	}
	commentsJSON, err := json.Marshal(comments)
	if err != nil {
		return nil, fmt.Errorf("encode GitHub comments: %w", err)
	}
	changed, err := q.CommentsWithChangedCreation(ctx, dbgen.CommentsWithChangedCreationParams{Repo: cycle.Repo, Comments: commentsJSON})
	if err != nil {
		return nil, fmt.Errorf("check GitHub comment creation times: %w", err)
	}
	if len(changed) > 0 {
		return nil, fmt.Errorf("%w: comment %d", githubsync.ErrCreationTimeChanged, changed[0])
	}
	fresh, err := q.NewCommentIDs(ctx, dbgen.NewCommentIDsParams{Repo: cycle.Repo, Comments: commentsJSON})
	if err != nil {
		return nil, fmt.Errorf("find the GitHub comments first seen: %w", err)
	}
	if err := q.ClearCommentsNotSeen(ctx, dbgen.ClearCommentsNotSeenParams{Repo: cycle.Repo, CommentIds: seen}); err != nil {
		return nil, fmt.Errorf("clear GitHub comment absence: %w", err)
	}
	if err := q.InsertCommentVersions(ctx, dbgen.InsertCommentVersionsParams{Repo: cycle.Repo, At: at, Comments: commentsJSON}); err != nil {
		return nil, fmt.Errorf("write GitHub comments: %w", err)
	}
	if err := q.MarkCommentsNotSeen(ctx, dbgen.MarkCommentsNotSeenParams{Repo: cycle.Repo, At: at, ListedPulls: open, ListedComments: seen}); err != nil {
		return nil, fmt.Errorf("mark GitHub comments not seen: %w", err)
	}
	return fresh, nil
}

// RecordFailure implements githubsync.Store. One upsert is one transaction,
// and it locks the repository row like a cycle does.
func (s *Store) RecordFailure(ctx context.Context, failure githubsync.Failure) error {
	if err := failure.Validate(); err != nil {
		return err
	}
	status, err := int32Of(failure.Status)
	if err != nil {
		return err
	}
	err = dbgen.New(s.pool).UpsertRepositoryFailure(ctx, dbgen.UpsertRepositoryFailureParams{
		Repo: failure.Repo, InstallationID: failure.InstallationID, LastError: string(failure.Kind), LastStatus: status, WaitUntil: timestamp(failure.WaitUntil),
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
		pulls[i] = pullState(row)
	}
	return pulls, nil
}

func pullState(row dbgen.ListGitHubPullsRow) githubsync.PullState {
	return githubsync.PullState{Number: int(row.Number), HeadSHA: row.HeadSha, HeadRef: row.HeadRef, BaseRef: row.BaseRef, BaseSHA: row.BaseSha,
		UpdatedAt: observedTime(row.UpdatedAt), FirstSeen: observedTime(row.FirstSeen), LastSeen: observedTime(row.LastSeen), NotOpenSince: observedTime(row.NotOpenSince),
		HeadRepo: row.HeadRepo.String}
}

// Comments implements githubsync.Store.
func (s *Store) Comments(ctx context.Context, repo string, number int) ([]githubsync.CommentVersion, error) {
	if err := githubsync.ValidatePullNumber(number); err != nil {
		return nil, err
	}
	pull, err := int32Of(number)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.pool).ListGitHubComments(ctx, dbgen.ListGitHubCommentsParams{Repo: repo, PullNumber: pull})
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
			CreatedAt: observedTime(row.CreatedAt), UpdatedAt: observedTime(row.UpdatedAt), Body: string(row.Body), BodyDigest: digest,
			FirstSeen: observedTime(row.FirstSeen), NotSeenSince: observedTime(row.NotSeenSince)}
	}
	return versions, nil
}

// withRepository runs fn in one transaction that holds the repository's row, so the merge-facts writes serialize with
// a cycle like RecordCycle does. fn gets the closed watermark. found is false, and fn does not run, for a repository
// that has no row yet.
func (s *Store) withRepository(ctx context.Context, repo string, fn func(q *dbgen.Queries, watermark pgtype.Timestamptz) error) (found bool, err error) {
	err = inTx(ctx, s.pool, "GitHub", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		watermark, err := q.LockGitHubRepository(ctx, repo)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock GitHub repository: %w", err)
		}
		found = true
		return fn(q, watermark)
	})
	return found, err
}

// AwaitingResolution implements githubsync.Store.
func (s *Store) AwaitingResolution(ctx context.Context, repo string) ([]githubsync.PullState, error) {
	rows, err := dbgen.New(s.pool).ListAwaitingResolution(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("read GitHub pulls awaiting resolution: %w", err)
	}
	pulls := make([]githubsync.PullState, len(rows))
	for i, row := range rows {
		pulls[i] = pullState(dbgen.ListGitHubPullsRow(row))
	}
	return pulls, nil
}

// RecordResolution implements githubsync.Store.
func (s *Store) RecordResolution(ctx context.Context, repo string, r githubsync.Resolution) error {
	if err := r.Validate(); err != nil {
		return err
	}
	number, err := int32Of(r.Number)
	if err != nil {
		return err
	}
	written := int64(0)
	found, err := s.withRepository(ctx, repo, func(q *dbgen.Queries, _ pgtype.Timestamptz) (err error) {
		written, err = q.RecordResolution(ctx, dbgen.RecordResolutionParams{Repo: repo, Number: number, NotOpenSince: timestamp(r.NotOpenSince),
			HeadSha: r.HeadSHA, HeadRef: r.HeadRef, BaseRef: r.BaseRef, BaseSha: r.BaseSHA, HeadRepo: r.HeadRepo,
			Merged: r.Merged, MergedAt: timestamp(r.MergedAt), MergeCommitSha: r.MergeCommitSHA, ResolvedAt: timestamp(r.ResolvedAt)})
		return err
	})
	if err != nil {
		return fmt.Errorf("write GitHub pull resolution: %w", err)
	}
	if !found || written == 0 {
		return fmt.Errorf("%w: pull %d of %s is not absent since %v", githubsync.ErrStaleResolution, r.Number, repo, r.NotOpenSince)
	}
	return nil
}

// RecordClosed implements githubsync.Store.
func (s *Store) RecordClosed(ctx context.Context, repo string, closed []githubsync.ClosedPull, at time.Time) error {
	if err := githubsync.ValidateClosed(closed, at); err != nil {
		return err
	}
	found, err := s.withRepository(ctx, repo, func(q *dbgen.Queries, watermark pgtype.Timestamptz) error {
		fresh, latest := closedBatch(closed, watermark)
		if !watermark.Valid { // the first call only sets the watermark, one microsecond past its newest item so that the listing is history; an empty listing sets the epoch, so the next call is not a first one
			if latest.IsZero() {
				latest = time.Unix(0, 0)
			}
			return q.SetClosedWatermark(ctx, dbgen.SetClosedWatermarkParams{Repo: repo, Watermark: timestamp(latest.Add(time.Microsecond))})
		}
		if len(fresh) > 0 {
			pulls, err := json.Marshal(fresh)
			if err != nil {
				return fmt.Errorf("encode closed GitHub pulls: %w", err)
			}
			if err := q.InsertClosedPulls(ctx, dbgen.InsertClosedPullsParams{Repo: repo, At: timestamp(at), Pulls: pulls}); err != nil {
				return err
			}
		}
		if latest.After(watermark.Time) {
			return q.SetClosedWatermark(ctx, dbgen.SetClosedWatermarkParams{Repo: repo, Watermark: timestamp(latest)})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write closed GitHub pulls: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: repository %s has no recorded cycle", githubsync.ErrInvalidObservation, repo)
	}
	return nil
}

// closedBatch returns the merged items the watermark has not covered and the greatest updated_at of the listing.
func closedBatch(closed []githubsync.ClosedPull, watermark pgtype.Timestamptz) (fresh []pullRow, latest time.Time) {
	for _, item := range closed {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
		if watermark.Valid && !item.MergedAt.IsZero() && !item.UpdatedAt.Before(watermark.Time) {
			pull := item.ObservedPull
			fresh = append(fresh, pullRow{Number: pull.Number, HeadSHA: pull.HeadSHA, HeadRef: pull.HeadRef, BaseRef: pull.BaseRef, BaseSHA: pull.BaseSHA, UpdatedAt: pull.UpdatedAt, HeadRepo: pull.HeadRepo})
		}
	}
	return fresh, latest
}

// Merged implements githubsync.Store.
func (s *Store) Merged(ctx context.Context, repo string) ([]githubsync.MergedPull, error) {
	rows, err := dbgen.New(s.pool).ListMergedPulls(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("read merged GitHub pulls: %w", err)
	}
	merged := make([]githubsync.MergedPull, len(rows))
	for i, row := range rows {
		state := githubsync.PullState{Number: int(row.Number), HeadSHA: row.HeadSha, HeadRef: row.HeadRef, BaseRef: row.BaseRef, BaseSHA: row.BaseSha,
			UpdatedAt: observedTime(row.UpdatedAt), FirstSeen: observedTime(row.FirstSeen), LastSeen: observedTime(row.LastSeen), NotOpenSince: observedTime(row.NotOpenSince),
			HeadRepo: row.HeadRepo.String}
		merged[i] = githubsync.MergedPull{PullState: state, Processed: row.MergeProcessed, Resolution: githubsync.Resolution{
			Number: state.Number, NotOpenSince: observedTime(row.ResolvedFor), Merged: true, MergedAt: observedTime(row.MergedAt), MergeCommitSHA: row.MergeCommitSha.String,
			HeadSHA: state.HeadSHA, HeadRef: state.HeadRef, BaseRef: state.BaseRef, BaseSHA: state.BaseSHA, HeadRepo: state.HeadRepo, ResolvedAt: observedTime(row.ResolvedAt)}}
	}
	return merged, nil
}

// MarkProcessed implements githubsync.Store.
func (s *Store) MarkProcessed(ctx context.Context, repo string, number int) error {
	if err := githubsync.ValidatePullNumber(number); err != nil {
		return err
	}
	pull, err := int32Of(number)
	if err != nil {
		return err
	}
	written := int64(0)
	found, err := s.withRepository(ctx, repo, func(q *dbgen.Queries, _ pgtype.Timestamptz) (err error) {
		written, err = q.MarkMergeProcessed(ctx, dbgen.MarkMergeProcessedParams{Repo: repo, Number: pull})
		return err
	})
	if err != nil {
		return fmt.Errorf("mark GitHub pull processed: %w", err)
	}
	if !found || written == 0 {
		return fmt.Errorf("%w: pull %d of %s is not stored", githubsync.ErrInvalidObservation, number, repo)
	}
	return nil
}
