package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
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

	// receiptNotApplicable is the reason of the last run of a head whose published receipt no longer applies, or whose level turned off.
	receiptNotApplicable = "receipt-not-applicable"
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

// withdrawnReceipt is the one last run of a head whose receipt does not apply (it changes no file that the canonical include matches, or the level is off) but that
// already has a published Receipt check: neutral, with the counts at zero, and nothing after it while that stays so. A head that never had one gets none, and a
// head whose receipt the run did not decide (its policy could not be read, its revision expects another canonical) keeps the check it has.
func (j *checksJob) withdrawnReceipt(ctx context.Context, s *pullState, jobs headJobs) (receiptDecision, error) {
	if !jobs.receiptDecided {
		return receiptDecision{}, nil
	}
	record, found, err := j.receipts.Current(ctx, s.p.RepositoryID, s.pull.Number)
	if err != nil {
		return receiptDecision{}, fmt.Errorf("read the Receipt check: %w", err)
	}
	if !found || record.HeadSHA != s.pull.HeadSHA {
		return receiptDecision{}, nil
	}
	return receiptDecision{publish: true, check: execution.ReceiptCheck{RepositoryID: s.p.RepositoryID, PullNumber: s.pull.Number, HeadSHA: s.pull.HeadSHA,
		State: execution.CheckNeutral, Reason: receiptNotApplicable, Reference: record.Reference}}, nil
}

// receiptCheck decides the Receipt check of the head from its receipt job: in progress while it is pending or has no job yet, a failure when it is complete
// with a failing or missing test or a file that does not parse, a success otherwise, and neutral when it is unavailable, which the owner's unblock marks
// without changing it.
func (j *checksJob) receiptCheck(ctx context.Context, s *pullState, jobs headJobs) (receiptDecision, error) {
	if !jobs.receiptApplies {
		return j.withdrawnReceipt(ctx, s, jobs)
	}
	run, err := j.reads().receipt(ctx, jobs.receipt, jobs.hasReceipt)
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
	if _, _, err := p.output(key); err != nil { // rendered when it is built, as every github_check message is: a state or a combination the renderer refuses fails this step
		return governance.OutboxMessage{}, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the Receipt check message: %w", err)
	}
	return governance.OutboxMessage{Key: key, Kind: receiptCheckKind, Payload: payload}, nil
}
