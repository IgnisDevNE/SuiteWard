package executiontest

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

var errNotImplemented = errors.New("executiontest: not implemented")

// Memory is an in-memory execution.Store.
type Memory struct{}

var _ execution.Store = (*Memory)(nil)

// NewMemory returns an empty store that records its entries in ledger.
func NewMemory(ledger commands.Store) *Memory { return &Memory{} }

func (m *Memory) Enqueue(context.Context, execution.NewJob) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (m *Memory) Lease(context.Context, execution.LeaseRequest) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (m *Memory) Heartbeat(context.Context, int64, string, time.Time) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

func (m *Memory) Complete(context.Context, int64, execution.Result, time.Time, time.Duration) (execution.Outcome, error) {
	return execution.Outcome{}, errNotImplemented
}

func (m *Memory) GitHubFailure(context.Context, int64, time.Time, time.Duration) (execution.State, error) {
	return "", errNotImplemented
}

func (m *Memory) Cancel(context.Context, int64, execution.Reason, time.Time) (bool, error) {
	return false, errNotImplemented
}

func (m *Memory) Expire(context.Context, execution.ExpireParams) (int, error) {
	return 0, errNotImplemented
}

func (m *Memory) Job(context.Context, int64) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (m *Memory) CurrentJob(context.Context, string) (execution.Job, bool, error) {
	return execution.Job{}, false, errNotImplemented
}

func (m *Memory) HeadJobs(context.Context, int64, int, string, execution.Plan) ([]execution.Job, error) {
	return nil, errNotImplemented
}

func (m *Memory) Result(context.Context, int64) (execution.StoredResult, bool, error) {
	return execution.StoredResult{}, false, errNotImplemented
}

func (m *Memory) Tests(context.Context, int64) ([]execution.Test, error) {
	return nil, errNotImplemented
}

func (m *Memory) Counts(context.Context) (map[execution.State]int, error) {
	return nil, errNotImplemented
}

func (m *Memory) Unblocked(context.Context, int64, time.Time) (bool, error) {
	return false, errNotImplemented
}

func (m *Memory) RecordApproval(context.Context, commands.Entry, *governance.OutboxMessage, execution.ApprovalGate) error {
	return errNotImplemented
}

func (m *Memory) RecordUnblock(context.Context, commands.Entry, *governance.OutboxMessage, string, []int64) error {
	return errNotImplemented
}

// Gate returns the stored approval receipt of a comment.
func (m *Memory) Gate(context.Context, int64, int64) (execution.ApprovalGate, bool, error) {
	return execution.ApprovalGate{}, false, errNotImplemented
}

// Unblocks returns the jobs that the unblock rows of a comment name, sorted.
func (m *Memory) Unblocks(context.Context, int64, int64) ([]int64, error) {
	return nil, errNotImplemented
}

// ReceiptMemory is an in-memory execution.ReceiptLedger.
type ReceiptMemory struct{}

var _ execution.ReceiptLedger = (*ReceiptMemory)(nil)

// NewReceiptMemory returns an empty ledger.
func NewReceiptMemory() *ReceiptMemory { return &ReceiptMemory{} }

func (m *ReceiptMemory) Current(context.Context, int64, int) (execution.ReceiptRecord, bool, error) {
	return execution.ReceiptRecord{}, false, errNotImplemented
}

func (m *ReceiptMemory) Publish(context.Context, execution.ReceiptCheck, time.Time, func(int64) (governance.OutboxMessage, error)) (execution.ReceiptRecord, bool, error) {
	return execution.ReceiptRecord{}, false, errNotImplemented
}

// OutboxKeys returns the key of every committed message, sorted.
func (m *ReceiptMemory) OutboxKeys(context.Context) ([]string, error) { return nil, errNotImplemented }

// FailMessageAt marks a message as failed at finishedAt.
func (m *ReceiptMemory) FailMessageAt(context.Context, string, time.Time) error {
	return errNotImplemented
}
