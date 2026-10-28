package postgres

import (
	"cmp"
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

// RecordUnblock implements execution.Store in one transaction: it locks the jobs in id order, compares each with what the caller observed, ends the open ones
// for no-executor, and writes the ledger entry, its acknowledgment and one unblock row for each lifted job. A command that already lifted jobs is found by its
// rows, after the locks (a concurrent attempt of the same command waits for the first and finds them), and must lift the same jobs at the same time: it
// is success, and the jobs are not looked at again, since they ended with it.
func (s executionStore) RecordUnblock(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, headSHA string, jobs []execution.UnblockJob, at time.Time) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := execution.ValidateUnblock(e, headSHA, jobs, at); err != nil {
		return err
	}
	if ack != nil {
		if err := ack.Validate(); err != nil {
			return err
		}
	}
	e, at = e.AtStoredPrecision(), execution.Stored(at)
	pull, err := int32Of(e.PullNumber)
	if err != nil {
		return err
	}
	jobs = slices.SortedFunc(slices.Values(jobs), func(a, b execution.UnblockJob) int { return cmp.Compare(a.ID, b.ID) })
	return inTx(ctx, s.pool, "unblock", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		locked := make([]execution.Job, len(jobs))
		for i, o := range jobs {
			job, err := lockJob(ctx, q, o.ID)
			if errors.Is(err, execution.ErrNotFound) {
				return fmt.Errorf("%w: job %d does not exist", execution.ErrInvalidRequest, o.ID)
			}
			if err != nil {
				return err
			}
			locked[i] = job
		}
		existing, err := q.ListExecutionUnblocks(ctx, dbgen.ListExecutionUnblocksParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID})
		if err != nil {
			return fmt.Errorf("read the unblock rows: %w", err)
		}
		if len(existing) > 0 {
			if !sameUnblock(existing, jobs, at) {
				return fmt.Errorf("%w: comment %d of repository %d unblocked other jobs", commands.ErrConflict, e.CommentID, e.RepositoryID)
			}
			_, err := recordEntry(ctx, q, e, pull, ack)
			return err
		}
		if err := checkUnblockable(jobs, locked, e, headSHA); err != nil {
			return err
		}
		if _, err := recordEntry(ctx, q, e, pull, ack); err != nil {
			return err
		}
		for i, o := range jobs {
			if next := locked[i].UnblockEnded(at); next.State != locked[i].State {
				if err := storeJob(ctx, q, next); err != nil {
					return err
				}
			}
			if _, err := q.InsertExecutionUnblock(ctx, dbgen.InsertExecutionUnblockParams{RepositoryID: e.RepositoryID, CommentID: e.CommentID, JobID: o.ID, PullNumber: pull,
				HeadSha: headSHA, DecidedAt: timestamp(at)}); err != nil {
				return executionWriteError("write the unblock row", err)
			}
		}
		return nil
	})
}

// sameUnblock reports whether the rows of a command, in job order, are those of the jobs it names, all decided at the time.
func sameUnblock(rows []dbgen.ListExecutionUnblocksRow, jobs []execution.UnblockJob, at time.Time) bool {
	if len(rows) != len(jobs) {
		return false
	}
	for i, row := range rows {
		if row.JobID != jobs[i].ID || !observedTime(row.DecidedAt).Equal(at) {
			return false
		}
	}
	return true
}

// checkUnblockable is execution.ErrInvalidRequest unless every locked job is of the repository, the pull request and the head, and execution.ErrJobChanged
// unless every one is still as the caller observed it.
func checkUnblockable(jobs []execution.UnblockJob, locked []execution.Job, e commands.Entry, headSHA string) error {
	for _, job := range locked {
		if job.RepositoryID != e.RepositoryID || job.PullNumber != e.PullNumber || job.HeadSHA != headSHA {
			return fmt.Errorf("%w: job %d is not a job of this head and pull request", execution.ErrInvalidRequest, job.ID)
		}
	}
	for i, job := range locked {
		if !job.Observed(jobs[i]) {
			return fmt.Errorf("%w: job %d", execution.ErrJobChanged, job.ID)
		}
	}
	return nil
}
