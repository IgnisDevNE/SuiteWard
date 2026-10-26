//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"time"

	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// TestExecutionConformance runs the execution suite against PostgreSQL, as the application role the service connects as. Every case
// gets its own freshly migrated schema, because history cannot be truncated.
func TestExecutionConformance(t *testing.T) {
	executiontest.RunConformance(t, func(t *testing.T) executiontest.Env {
		w := newPGWorld(t)
		return executiontest.Env{
			Store: w.store.Execution(), Receipts: w.store.ReceiptChecks(), Ledger: w.store,
			OutboxKeys: func(ctx context.Context) ([]string, error) {
				rows, err := w.pool.Query(ctx, "SELECT key FROM outbox ORDER BY key COLLATE \"C\"")
				if err != nil {
					return nil, err
				}
				return pgx.CollectRows(rows, pgx.RowTo[string])
			},
			FailMessage: func(ctx context.Context, key string, finishedAt time.Time) error {
				_, err := w.pool.Exec(ctx, "UPDATE outbox SET state = 'failed', finished_at = $2 WHERE key = $1", key, finishedAt)
				return err
			},
			Gate: func(ctx context.Context, repositoryID, commentID int64) (execution.ApprovalGate, bool, error) {
				var head, level, run string
				var job *int64
				var summary *string
				var gate execution.ApprovalGate
				err := w.pool.QueryRow(ctx, "SELECT head_sha, receipt_level, run_state, job_id, summary, unblocked FROM approval_receipts WHERE repository_id = $1 AND comment_id = $2",
					repositoryID, commentID).Scan(&head, &level, &run, &job, &summary, &gate.Unblocked)
				if errors.Is(err, pgx.ErrNoRows) {
					return execution.ApprovalGate{}, false, nil
				}
				if err != nil {
					return execution.ApprovalGate{}, false, err
				}
				parsed, err := domain.ParseReceiptLevel(level)
				if err != nil {
					return execution.ApprovalGate{}, false, err
				}
				gate.HeadSHA, gate.ReceiptLevel, gate.RunState = head, parsed, execution.RunState(run)
				if job != nil {
					gate.JobID = *job
				}
				if summary != nil {
					gate.Summary = execution.Summary(*summary)
				}
				return gate, true, nil
			},
			Unblocks: func(ctx context.Context, repositoryID, commentID int64) ([]int64, error) {
				rows, err := w.pool.Query(ctx, "SELECT job_id FROM execution_unblocks WHERE repository_id = $1 AND comment_id = $2 ORDER BY job_id", repositoryID, commentID)
				if err != nil {
					return nil, err
				}
				return pgx.CollectRows(rows, pgx.RowTo[int64])
			},
		}
	})
}
