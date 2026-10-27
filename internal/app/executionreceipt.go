package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The SuiteWard / Receipt check (docs/contracts/runtime.md, "The SuiteWard / Receipt check"): the checks job reads the receipt job of the head, decides the
// state of the check from the domain (the conclusion never depends on a mode), writes it to the ledger of the check and, when it changed, the
// github_receipt_check message. The publisher and the texts are not here.

const (
	receiptCheckKind = "github_receipt_check"
	receiptCheckKey  = "receipt-check" // receipt-check:<repository id>:<pull>:<generation>

	receiptRunning  = "receipt-running"
	receiptFailing  = "receipt-failing"
	receiptComplete = "receipt-complete"
	receiptNotRun   = "receipt-not-run"
)

// receiptCheckCounts are the counts of the receipt that a check carries, every key always present: the tests by state, the excluded ones, the marker lines
// that the run saw and the changed test files that go cannot parse, each a blocking failure.
type receiptCheckCounts struct {
	Detects  int `json:"detects"`
	Inert    int `json:"inert"`
	NewAPI   int `json:"newApi"`
	Fails    int `json:"fails"`
	Missing  int `json:"missing"`
	Flaky    int `json:"flaky"`
	Skipped  int `json:"skipped"`
	Excluded int `json:"excluded"`
	Markers  int `json:"markers"`
	Unparsed int `json:"unparsed"`
}

// receiptCheckPayload is the payload of receipt-check:<repository id>:<pull>:<generation>: the decided state of the receipt of one head. It holds identifiers,
// codes and counts, never text from the repository.
type receiptCheckPayload struct {
	commentTarget
	HeadSHA     string             `json:"headSha"`
	Generation  int64              `json:"generation"`
	State       string             `json:"state"`
	Reason      string             `json:"reason"` // receipt-running, receipt-failing, receipt-complete or receipt-not-run
	Reference   string             `json:"reference"`
	Tier        string             `json:"tier"`
	Counts      receiptCheckCounts `json:"counts"`
	Unavailable string             `json:"unavailable,omitempty"` // for a neutral check: why the job did not run
	Unblocked   bool               `json:"unblocked,omitempty"`
}

// receiptDecision is the Receipt check of a head as the job decided it. publish is false for a head with no receipt (the level is off, the head has no
// revision or the receipt does not apply): nothing is published then, as the domain's ConclusionNone says.
type receiptDecision struct {
	publish     bool
	check       execution.ReceiptCheck
	counts      receiptCheckCounts
	unavailable execution.Reason
}

// receiptStateOf are the receipt states as the store keeps them.
var receiptStateOf = map[string]domain.ReceiptState{"excluded": domain.ReceiptExcluded, "missing": domain.ReceiptMissing, "flaky": domain.ReceiptFlaky, "skipped": domain.ReceiptSkipped,
	"fails": domain.ReceiptFails, "new_api": domain.ReceiptNewAPI, "detects": domain.ReceiptDetects, "inert": domain.ReceiptInert}

// receiptRunOf is the status of the receipt run of the head and, for a complete one, its counts. No job reads as pending.
type receiptRunOf struct {
	status  domain.RunStatus
	counts  domain.ReceiptCounts
	markers int
}

// readReceiptRun reads the receipt run of the head: the status of its job and, for a complete one, the tests by state, the marker lines and the files that
// do not parse.
func (j *checksJob) readReceiptRun(ctx context.Context, jobs headJobs) (receiptRunOf, error) {
	if !jobs.hasReceipt {
		return receiptRunOf{status: domain.RunPending}, nil
	}
	switch jobs.receipt.State {
	case execution.StatePending, execution.StateLeased:
		return receiptRunOf{status: domain.RunPending}, nil
	case execution.StateUnavailable:
		return receiptRunOf{status: domain.RunUnavailable}, nil
	case execution.StateComplete:
		return j.readCompleteReceipt(ctx, jobs.receipt)
	}
	return receiptRunOf{}, fmt.Errorf("the receipt job %d of the head is %s: only a job that is not cancelled is read", jobs.receipt.ID, jobs.receipt.State)
}

func (j *checksJob) readCompleteReceipt(ctx context.Context, job execution.Job) (receiptRunOf, error) {
	tests, err := j.executions.Tests(ctx, job.ID)
	if err != nil {
		return receiptRunOf{}, fmt.Errorf("read the tests of job %d: %w", job.ID, err)
	}
	states := make([]domain.ReceiptState, len(tests))
	for i, t := range tests {
		state, known := receiptStateOf[t.State]
		if !known {
			return receiptRunOf{}, fmt.Errorf("job %d stores the test state %q, which is no state of a receipt", job.ID, t.State)
		}
		states[i] = state
	}
	counts, err := domain.CountReceipt(states)
	if err != nil {
		return receiptRunOf{}, err
	}
	result, found, err := j.executions.Result(ctx, job.ID)
	if err != nil {
		return receiptRunOf{}, fmt.Errorf("read the result of job %d: %w", job.ID, err)
	}
	if !found {
		return receiptRunOf{}, fmt.Errorf("the complete job %d has no results row", job.ID)
	}
	counts.Unparsed = len(result.Unparsed)
	return receiptRunOf{status: domain.RunComplete, counts: counts, markers: len(result.Markers)}, nil
}

// receiptCheck decides the Receipt check of the head from its receipt job: in progress while it is pending or has no job yet, a failure when it is complete
// with a failing or missing test or a file that does not parse, a success otherwise, and neutral when it is unavailable, which the owner's unblock marks
// without changing it.
func (j *checksJob) receiptCheck(ctx context.Context, s *pullState, jobs headJobs) (receiptDecision, error) {
	if !jobs.receiptApplies {
		return receiptDecision{}, nil
	}
	run, err := j.readReceiptRun(ctx, jobs)
	if err != nil {
		return receiptDecision{}, err
	}
	conclusion, err := domain.ReceiptConclusion(run.status, run.counts)
	if err != nil {
		return receiptDecision{}, fmt.Errorf("decide the Receipt check: %w", err)
	}
	check := execution.ReceiptCheck{RepositoryID: s.p.RepositoryID, PullNumber: s.pull.Number, HeadSHA: s.pull.HeadSHA, Reference: jobs.reference}
	switch conclusion {
	case domain.ConclusionInProgress:
		check.State, check.Reason = execution.CheckInProgress, receiptRunning
	case domain.ConclusionFailure:
		check.State, check.Reason = execution.CheckFailure, receiptFailing
	case domain.ConclusionSuccess:
		check.State, check.Reason = execution.CheckSuccess, receiptComplete
	case domain.ConclusionNeutral:
		check.State, check.Reason = execution.CheckNeutral, receiptNotRun
	default: // ConclusionNone is a receipt that does not apply, which was told apart above
		return receiptDecision{}, fmt.Errorf("the Receipt check of an applicable receipt has the conclusion %d", conclusion)
	}
	decision := receiptDecision{publish: true, check: check, counts: receiptCheckCounts{Detects: run.counts.Detects, Inert: run.counts.Inert, NewAPI: run.counts.NewAPI,
		Fails: run.counts.Fails, Missing: run.counts.Missing, Flaky: run.counts.Flaky, Skipped: run.counts.Skipped, Excluded: run.counts.Excluded, Markers: run.markers,
		Unparsed: run.counts.Unparsed}}
	if jobs.hasReceipt {
		decision.check.JobID = jobs.receipt.ID
	}
	if run.status == domain.RunUnavailable {
		decision.unavailable = jobs.receipt.Reason
		if decision.check.Unblocked, err = j.executions.Unblocked(ctx, jobs.receipt.ID, s.enrollment.EnrolledAt); err != nil {
			return receiptDecision{}, fmt.Errorf("read the unblock of job %d: %w", jobs.receipt.ID, err)
		}
	}
	return decision, nil
}

// newReceiptCheckMessage builds the github_receipt_check message of generation for the decided Receipt check d of a pull request of repo (owner/name).
func newReceiptCheckMessage(repo string, d receiptDecision, tier string, generation int64) (governance.OutboxMessage, error) {
	p := receiptCheckPayload{commentTarget: commentTarget{RepositoryID: d.check.RepositoryID, Repo: repo, Pull: d.check.PullNumber}, HeadSHA: d.check.HeadSHA, Generation: generation,
		State: string(d.check.State), Reason: d.check.Reason, Reference: d.check.Reference, Tier: tier, Counts: d.counts, Unavailable: string(d.unavailable), Unblocked: d.check.Unblocked}
	key := fmt.Sprintf("%s:%d:%d:%d", receiptCheckKey, d.check.RepositoryID, d.check.PullNumber, generation)
	switch {
	case p.validate() != nil:
		return governance.OutboxMessage{}, keyError(receiptCheckKind, key, p.validate().Error())
	case !headPattern.MatchString(p.HeadSHA) || p.Generation < 1:
		return governance.OutboxMessage{}, keyError(receiptCheckKind, key, "the head or the generation is invalid")
	case tier != owner.TierIsolated && tier != owner.TierCoLocated:
		return governance.OutboxMessage{}, keyError(receiptCheckKind, key, "the tier is unknown")
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the Receipt check message: %w", err)
	}
	return governance.OutboxMessage{Key: key, Kind: receiptCheckKind, Payload: payload}, nil
}
