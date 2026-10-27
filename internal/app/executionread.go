package app

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// executionReader reads what a gate reads of the execution of a head (docs/contracts/runtime.md, "The job a gate reads"). The checks job, the commands job and the
// receipt page read the job of a plan, its counts and the files the receipt evaluates through it, so that each rule is written once.
type executionReader struct {
	executions execution.Store
	contents   governance.ContentReader
	report     func(what string, cause error) error // how a failed read of the store is reported: each job keeps the class of error it has always returned
}

func opaqueRead(what string, cause error) error { return &opaque{what: what, cause: cause} }

func wrapRead(what string, cause error) error { return fmt.Errorf("%s: %w", what, cause) }

func (r executionReader) newestJob(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan, keep func(execution.Job) bool) (execution.Job, bool, error) {
	return execution.Job{}, false, nil
}

func (r executionReader) gateJob(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan, governing artifact.Digest) (execution.Job, bool, error) {
	return execution.Job{}, false, nil
}

// receiptRun is the receipt run of a head.
type receiptRun struct {
	job     execution.Job
	status  domain.RunStatus
	state   execution.RunState
	counts  domain.ReceiptCounts
	markers int
}

func (r executionReader) receipt(ctx context.Context, job execution.Job, found bool) (receiptRun, error) {
	return receiptRun{}, nil
}

// canonicalRun is the canonical run of a head.
type canonicalRun struct {
	status domain.RunStatus
	counts domain.CanonicalCounts
}

func (r executionReader) canonical(ctx context.Context, job execution.Job, found bool) (canonicalRun, error) {
	return canonicalRun{}, nil
}

func (r executionReader) evaluatedFiles(ctx context.Context, revision artifact.Digest, expected artifact.Manifest, selection domain.Selection) ([]string, error) {
	return nil, nil
}

func executionLevels(policy scope.Execution) (domain.CanonicalLevel, domain.ReceiptLevel, error) {
	return 0, 0, nil
}
