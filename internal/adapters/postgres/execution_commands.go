package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Unblocked implements execution.Store.
func (s executionStore) Unblocked(ctx context.Context, jobID int64, since time.Time) (bool, error) {
	unblocked, err := dbgen.New(s.pool).ExecutionJobUnblocked(ctx, dbgen.ExecutionJobUnblockedParams{JobID: jobID, Since: timestamp(since)})
	if err != nil {
		return false, fmt.Errorf("read whether job %d was unblocked: %w", jobID, err)
	}
	return unblocked, nil
}

func gateParams(e commands.Entry, gate execution.ApprovalGate) dbgen.InsertApprovalReceiptParams {
	var job pgtype.Int8
	if gate.JobID != 0 {
		job = pgtype.Int8{Int64: gate.JobID, Valid: true}
	}
	return dbgen.InsertApprovalReceiptParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID, HeadSha: gate.HeadSHA, ReceiptLevel: gate.ReceiptLevel.String(),
		RunState: string(gate.RunState), JobID: job, Summary: optionalText(string(gate.Summary)), Unblocked: gate.Unblocked, DecidedAt: timestamp(e.DecidedAt)}
}

// RecordApproval implements execution.Store in one transaction: the ledger entry, its acknowledgment and the receipt state at the approve.
// A gate that exists is compared with the new one, and another is commands.ErrConflict.
func (s executionStore) RecordApproval(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, gate execution.ApprovalGate) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := gate.Validate(); err != nil {
		return err
	}
	if e.Disposition != commands.Processed && e.Disposition != commands.ReceiptRefused {
		return fmt.Errorf("%w: the gate of an approve is recorded with processed or receipt-refused, not %s", execution.ErrInvalidRequest, e.Disposition)
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
	return inTx(ctx, s.pool, "approval receipt", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		if _, err := recordEntry(ctx, q, e, pull, ack); err != nil {
			return err
		}
		params := gateParams(e, gate)
		inserted, err := q.InsertApprovalReceipt(ctx, params)
		if err != nil {
			return executionWriteError("write the approval receipt", err)
		}
		if inserted == 1 {
			return nil
		}
		stored, err := q.GetApprovalReceipt(ctx, dbgen.GetApprovalReceiptParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID})
		if err != nil {
			return fmt.Errorf("read the approval receipt: %w", err)
		}
		if stored.HeadSha != params.HeadSha || stored.ReceiptLevel != params.ReceiptLevel || stored.RunState != params.RunState || stored.JobID != params.JobID ||
			stored.Summary != params.Summary || stored.Unblocked != params.Unblocked || !observedTime(stored.DecidedAt).Equal(e.DecidedAt) {
			return fmt.Errorf("%w: comment %d of repository %d has another approval receipt", commands.ErrConflict, e.CommentID, e.RepositoryID)
		}
		return nil
	})
}

// RecordUnblock implements execution.Store in one transaction: the ledger entry, its acknowledgment and one unblock row for each lifted
// job. Each job is checked first, and an unavailable job never changes, so the check cannot go stale. A command that already lifted
// jobs must lift the same ones.
func (s executionStore) RecordUnblock(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, headSHA string, jobs []execution.UnblockJob, at time.Time) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := execution.ValidateUnblock(e, headSHA, jobs, at); err != nil {
		return err
	}
	jobIDs := make([]int64, len(jobs))
	for i, job := range jobs {
		jobIDs[i] = job.ID
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
	lifted := slices.Sorted(slices.Values(jobIDs))
	return inTx(ctx, s.pool, "unblock", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		for _, id := range lifted {
			if err := checkUnblockable(ctx, q, id, e, pull, headSHA); err != nil {
				return err
			}
		}
		if _, err := recordEntry(ctx, q, e, pull, ack); err != nil {
			return err
		}
		existing, err := q.ListExecutionUnblocks(ctx, dbgen.ListExecutionUnblocksParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID})
		if err != nil {
			return fmt.Errorf("read the unblock rows: %w", err)
		}
		if len(existing) > 0 && !slices.Equal(existing, lifted) {
			return fmt.Errorf("%w: comment %d of repository %d unblocked other jobs", commands.ErrConflict, e.CommentID, e.RepositoryID)
		}
		for _, id := range lifted {
			if _, err := q.InsertExecutionUnblock(ctx, dbgen.InsertExecutionUnblockParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID, JobID: id, PullNumber: pull,
				HeadSha: headSHA, DecidedAt: timestamp(at)}); err != nil {
				return executionWriteError("write the unblock row", err)
			}
		}
		return nil
	})
}

// checkUnblockable is execution.ErrInvalidRequest unless the job exists, is unavailable, and is of the repository, the pull request and the head.
func checkUnblockable(ctx context.Context, q *dbgen.Queries, id int64, e commands.Entry, pull int32, headSHA string) error {
	job, err := q.GetExecutionJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: job %d does not exist", execution.ErrInvalidRequest, id)
	}
	if err != nil {
		return fmt.Errorf("read job %d: %w", id, err)
	}
	if job.State != string(execution.StateUnavailable) || job.RepositoryID != e.RepositoryID || job.PullNumber != pull || job.HeadSha != headSHA {
		return fmt.Errorf("%w: job %d is not an unavailable job of this head and pull request", execution.ErrInvalidRequest, id)
	}
	return nil
}
