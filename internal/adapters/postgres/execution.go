package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

var errNotImplemented = errors.New("postgres: not implemented")

// executionStore is execution.Store over the pool of a Store.
type executionStore struct{ pool *pgxpool.Pool }

var _ execution.Store = executionStore{}

// Execution returns the execution ledger of this database.
func (s *Store) Execution() execution.Store { return executionStore{pool: s.pool} }

func (executionStore) Enqueue(context.Context, execution.NewJob) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (executionStore) Lease(context.Context, execution.LeaseRequest) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (executionStore) Heartbeat(context.Context, int64, string, time.Time) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

func (executionStore) Complete(context.Context, int64, execution.Result, time.Time, time.Duration) (execution.Outcome, error) {
	return execution.Outcome{}, errNotImplemented
}

func (executionStore) GitHubFailure(context.Context, int64, time.Time, time.Duration) (execution.State, error) {
	return "", errNotImplemented
}

func (executionStore) Cancel(context.Context, int64, execution.Reason, time.Time) (bool, error) {
	return false, errNotImplemented
}

func (executionStore) Expire(context.Context, execution.ExpireParams) (int, error) {
	return 0, errNotImplemented
}

func (executionStore) Job(context.Context, int64) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (executionStore) CurrentJob(context.Context, string) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (executionStore) HeadJobs(context.Context, int64, int, string, execution.Plan) ([]execution.Job, error) {
	return nil, errNotImplemented
}

func (executionStore) Result(context.Context, int64) (execution.StoredResult, bool, error) {
	return execution.StoredResult{}, false, errNotImplemented
}

func (executionStore) Tests(context.Context, int64) ([]execution.Test, error) {
	return nil, errNotImplemented
}

func (executionStore) Counts(context.Context) (map[execution.State]int, error) {
	return nil, errNotImplemented
}

func (executionStore) Unblocked(context.Context, int64, time.Time) (bool, error) {
	return false, errNotImplemented
}

func (executionStore) RecordApproval(context.Context, commands.Entry, *governance.OutboxMessage, execution.ApprovalGate) error {
	return errNotImplemented
}

func (executionStore) RecordUnblock(context.Context, commands.Entry, *governance.OutboxMessage, string, []int64) error {
	return errNotImplemented
}

// receiptLedger is execution.ReceiptLedger over the pool of a Store.
type receiptLedger struct{ pool *pgxpool.Pool }

var _ execution.ReceiptLedger = receiptLedger{}

// ReceiptChecks returns the ledger of the Receipt check of this database.
func (s *Store) ReceiptChecks() execution.ReceiptLedger { return receiptLedger{pool: s.pool} }

func (receiptLedger) Current(context.Context, int64, int) (execution.ReceiptRecord, bool, error) {
	return execution.ReceiptRecord{}, false, errNotImplemented
}

func (receiptLedger) Publish(context.Context, execution.ReceiptCheck, time.Time, func(int64) (governance.OutboxMessage, error)) (execution.ReceiptRecord, bool, error) {
	return execution.ReceiptRecord{}, false, errNotImplemented
}
