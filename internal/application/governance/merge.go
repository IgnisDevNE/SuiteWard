package governance

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// MergeKind is the one outcome recorded for a merged pull request.
type MergeKind string

const (
	MergePromoted        MergeKind = "promoted"
	MergeCorrected       MergeKind = "corrected"
	MergeNoChange        MergeKind = "no-change"
	MergeNotTarget       MergeKind = "not-target"
	MergeBeforeCanonical MergeKind = "before-canonical"
	MergeBlocked         MergeKind = "blocked"
)

// MergeReason says why a merge is blocked; it is set exactly when the kind is MergeBlocked.
type MergeReason string

const (
	// Decided by the use case.
	MergeReasonUnapprovedChange   MergeReason = "unapproved-change"
	MergeReasonInvalidSource      MergeReason = "invalid-source"
	MergeReasonAssessmentConflict MergeReason = "assessment-conflict"
	// The promotion decision's reasons.
	MergeReasonApprovalMissing         MergeReason = "approval-missing"
	MergeReasonDiffersFromApproved     MergeReason = "differs-from-approved"
	MergeReasonNotCurrent              MergeReason = "not-current"
	MergeReasonCanonicalChanged        MergeReason = "canonical-changed"
	MergeReasonPolicyChanged           MergeReason = "policy-changed"
	MergeReasonIntegrityNotPassed      MergeReason = "integrity-not-passed"
	MergeReasonIntegrationMismatch     MergeReason = "integration-mismatch"
	MergeReasonCorrectionContextReused MergeReason = "correction-context-reused"
	// Decided by the caller, who passes them as MergeRequest.Refusal.
	MergeReasonForkUnsupported       MergeReason = "fork-unsupported"
	MergeReasonSharedMergeCommit     MergeReason = "shared-merge-commit"
	MergeReasonIntegrationUnreadable MergeReason = "integration-unreadable"
	MergeReasonIntegrityFailed       MergeReason = "integrity-failed"
)

// MergeOutcome is what a merged pull request did to the canonical.
type MergeOutcome struct {
	Kind      MergeKind
	Reason    MergeReason
	Source    contract.SourceRevision     // the merge commit, "" when unreadable
	Version   contract.SuiteVersionID     // promoted or corrected: the new version; otherwise the canonical kept
	Corrects  contract.SuiteVersionID     // corrected only
	Reference *contract.ProposalReference // the PR's change proposal revision, when there is one
}

type MergeRequest struct {
	Project    contract.ProjectID
	Suite      contract.SuiteID
	Carrier    contract.ApprovalCarrierID   // github-pr:<repository id>/<pull>
	Source     contract.SourceRevision      // the merge commit; "" only with Refusal integration-unreadable
	Target     contract.IntegrationTargetID // branch:<base ref at the merge>
	ObservedAt time.Time                    // the pull's not_open_since (SuiteWard's clock)
	Tree       inventory.Tree               // the merge commit's tree; nil with a Refusal
	Refusal    MergeReason                  // a caller-decided blocked reason, or ""
	RecordedAt time.Time                    // microsecond precision
	// Message builds the outcome comment for the outcome. false writes no message.
	Message func(MergeOutcome) (OutboxMessage, bool, error)
}

type MergeResult struct {
	MergeOutcome
	Duplicate bool // the outcome was already recorded
}

// MergeWrite records the outcome of one merged pull request.
type MergeWrite struct {
	Carrier    contract.ApprovalCarrierID
	Outcome    MergeOutcome
	RecordedAt time.Time
}

var errMergeNotImplemented = errors.New("governance: merge outcome is not implemented")

func PromoteMerge(context.Context, UnitOfWork, ContentWriter, MergeRequest) (MergeResult, error) {
	return MergeResult{}, errMergeNotImplemented
}

func MergeOutcomeOf(context.Context, UnitOfWork, contract.ProjectID, contract.SuiteID, contract.ApprovalCarrierID) (MergeOutcome, bool, error) {
	return MergeOutcome{}, false, errMergeNotImplemented
}

// Validate reports ErrInvalidRequest when the write is internally inconsistent.
func (w MergeWrite) Validate() error { return errMergeNotImplemented }
