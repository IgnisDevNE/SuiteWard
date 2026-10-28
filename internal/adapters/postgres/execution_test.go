//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"time"

	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
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
			UnblockDecided: func(ctx context.Context, repositoryID, commentID, jobID int64) (time.Time, bool, error) {
				var decided time.Time
				err := w.pool.QueryRow(ctx, "SELECT decided_at FROM execution_unblocks WHERE repository_id = $1 AND comment_id = $2 AND job_id = $3", repositoryID, commentID, jobID).Scan(&decided)
				if errors.Is(err, pgx.ErrNoRows) {
					return time.Time{}, false, nil
				}
				return decided, err == nil, err
			},
		}
	})
}

// A lease reads GitHub through prepare while it holds the job, and the sweep holds the open jobs: neither may keep an approve that names the
// job from being recorded. The record of the approve checks its foreign key against the row, which a lock FOR UPDATE would make it wait for.
func TestALeaseHoldingAJobDoesNotBlockAnApprovalThatNamesIt(t *testing.T) {
	w := newPGWorld(t)
	store := w.store.Execution()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	digest := artifact.Hash([]byte("governing"))
	job, _, err := store.Enqueue(t.Context(), execution.NewJob{At: at, Spec: execution.Spec{RepositoryID: 10, PullNumber: 1, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1),
		GoverningDigest: digest, GoverningSource: "V1", DeclarationDigest: digest, CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff}})
	if err != nil {
		t.Fatal(err)
	}

	holding, release, leased := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, err := store.Lease(t.Context(), execution.LeaseRequest{Now: at, LeaseID: execLease(1), JobTimeout: time.Minute, GitHubWait: time.Hour},
			func(context.Context, execution.Job) error {
				close(holding) // the job is held while GitHub is read
				<-release
				return nil
			})
		leased <- err
	}()
	select { // a Lease that fails, or finds no job, before it calls prepare must fail the test and not hang it
	case <-holding:
	case err := <-leased:
		t.Fatalf("Lease ended before it held the job: %v", err)
	}
	approve := commands.Entry{RepositoryID: 10, CommentID: 100, PullNumber: 1, AuthorID: 7, CreatedAt: at, BodyDigest: artifact.Hash([]byte("body")), Disposition: commands.Processed,
		Operation: "consent-operation", DecidedAt: at}
	gate := execution.ApprovalGate{HeadSHA: job.HeadSHA, ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunPending, JobID: job.ID}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second) // a blocked record fails the test instead of hanging it
	defer cancel()
	recorded := store.RecordApproval(ctx, approve, nil, gate)
	close(release)
	if err := <-leased; err != nil {
		t.Fatalf("Lease = %v", err)
	}
	if recorded != nil {
		t.Fatalf("RecordApproval while a lease holds the job = %v; want it recorded without waiting for the lease", recorded)
	}
}
