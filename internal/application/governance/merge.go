package governance

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
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
	Version   contract.SuiteVersionID     // promoted or corrected: the new version; otherwise the canonical kept, empty for a blocked or before-canonical outcome when there is none
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

var mergeKinds = map[MergeKind]bool{MergePromoted: true, MergeCorrected: true, MergeNoChange: true, MergeNotTarget: true, MergeBeforeCanonical: true, MergeBlocked: true}

// callerReasons are the blocked reasons only the caller can know: the values of MergeRequest.Refusal.
var callerReasons = map[MergeReason]bool{MergeReasonForkUnsupported: true, MergeReasonSharedMergeCommit: true, MergeReasonIntegrationUnreadable: true, MergeReasonIntegrityFailed: true}

var mergeReasons = map[MergeReason]bool{
	MergeReasonUnapprovedChange: true, MergeReasonInvalidSource: true, MergeReasonAssessmentConflict: true, MergeReasonApprovalMissing: true,
	MergeReasonDiffersFromApproved: true, MergeReasonNotCurrent: true, MergeReasonCanonicalChanged: true, MergeReasonPolicyChanged: true,
	MergeReasonIntegrityNotPassed: true, MergeReasonIntegrationMismatch: true, MergeReasonCorrectionContextReused: true,
	MergeReasonForkUnsupported: true, MergeReasonSharedMergeCommit: true, MergeReasonIntegrationUnreadable: true, MergeReasonIntegrityFailed: true,
}

// promotionReasons maps the blocked reasons of the promotion decision; any other is a state the use case cannot reach.
var promotionReasons = map[contract.PromotionReason]MergeReason{
	contract.PromotionReasonApprovalMissing:         MergeReasonApprovalMissing,
	contract.PromotionReasonContextMismatch:         MergeReasonDiffersFromApproved,
	contract.PromotionReasonProposalNotCurrent:      MergeReasonNotCurrent,
	contract.PromotionReasonCanonicalChanged:        MergeReasonCanonicalChanged,
	contract.PromotionReasonPolicyChanged:           MergeReasonPolicyChanged,
	contract.PromotionReasonAssessmentMismatch:      MergeReasonIntegrityNotPassed,
	contract.PromotionReasonIntegrityNotPassed:      MergeReasonIntegrityNotPassed,
	contract.PromotionReasonIntegrationMismatch:     MergeReasonIntegrationMismatch,
	contract.PromotionReasonCorrectionContextReused: MergeReasonCorrectionContextReused,
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

// Validate reports ErrInvalidRequest when the write is internally inconsistent. Whether the version, the corrected
// version and the proposal revision it names exist is the store's to say.
func (w MergeWrite) Validate() error {
	o := w.Outcome
	reference := o.Reference
	switch {
	case blank(string(w.Carrier)):
		return fmt.Errorf("%w: a merge outcome names its carrier", ErrInvalidRequest)
	case !mergeKinds[o.Kind] || (o.Reason != "" && !mergeReasons[o.Reason]):
		return fmt.Errorf("%w: unknown merge kind %q or reason %q", ErrInvalidRequest, o.Kind, o.Reason)
	case (o.Kind == MergeBlocked) != (o.Reason != ""):
		return fmt.Errorf("%w: a reason is set exactly when the merge is blocked", ErrInvalidRequest)
	case (o.Kind == MergeCorrected) != (o.Corrects != ""):
		return fmt.Errorf("%w: a corrected version is set exactly when the merge is a correction", ErrInvalidRequest)
	case blank(string(o.Version)) && o.Kind != MergeBlocked && o.Kind != MergeBeforeCanonical:
		return fmt.Errorf("%w: only a blocked or before-canonical merge outcome may name no version", ErrInvalidRequest)
	case reference != nil && (blank(string(reference.ProposalID)) || blank(string(reference.RevisionID))):
		return fmt.Errorf("%w: a proposal reference names a proposal and a revision", ErrInvalidRequest)
	case w.RecordedAt.IsZero() || !w.RecordedAt.Equal(w.RecordedAt.Truncate(time.Microsecond)):
		return fmt.Errorf("%w: the recorded time needs microsecond precision", ErrInvalidRequest)
	}
	return nil
}

func validMergeRequest(uow UnitOfWork, writer ContentWriter, r MergeRequest) error {
	var problem string
	switch {
	case uow == nil:
		problem = "no unit of work"
	case blank(string(r.Project)) || blank(string(r.Suite)) || blank(string(r.Carrier)) || blank(string(r.Target)):
		problem = "a merge names its project, suite, carrier and target"
	case r.ObservedAt.IsZero() || r.RecordedAt.IsZero() || !r.RecordedAt.Equal(r.RecordedAt.Truncate(time.Microsecond)):
		problem = "a merge has an observed time and a recorded time of microsecond precision"
	case r.Message == nil:
		problem = "no message function"
	case r.Refusal != "" && !callerReasons[r.Refusal]:
		problem = fmt.Sprintf("%q is not a refusal", r.Refusal)
	case blank(string(r.Source)) && r.Refusal != MergeReasonIntegrationUnreadable:
		problem = "a merge commit is named unless it was unreadable"
	case r.Refusal == "" && r.Tree == nil:
		problem = "no tree"
	case r.Refusal == "" && writer == nil:
		problem = "no content writer"
	default:
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidRequest, problem)
}

// PromoteMerge decides and records the one outcome of a merged pull request. The tree is read and its content stored
// before the unit of work, so the Suite is not locked while a tree is read. Without a canonical, the merge of the
// bootstrap PR decides V1 and any other merge is before the canonical (decideFirstMerge).
func PromoteMerge(ctx context.Context, uow UnitOfWork, writer ContentWriter, request MergeRequest) (MergeResult, error) {
	if err := validMergeRequest(uow, writer, request); err != nil {
		return MergeResult{}, err
	}
	refusal := request.Refusal
	var inv inventory.Inventory
	var integrated contract.ProtectedContract
	if refusal == "" {
		var err error
		switch inv, err = inventory.Build(ctx, request.Tree); {
		case errors.Is(err, inventory.ErrInvalidTree) || errors.Is(err, inventory.ErrUnsafeEntry) || errors.Is(err, scope.ErrInvalidDeclaration):
			refusal = MergeReasonInvalidSource // a fact about the commit, as the declaration is the owner's
		case err != nil:
			return MergeResult{}, fmt.Errorf("inventory of the merge commit: %w", err)
		default:
			if integrated, err = inv.Contract(); err != nil {
				return MergeResult{}, fmt.Errorf("contract of the merge commit: %w", err)
			}
			if err := storeContent(ctx, request.Tree, integrated.Manifest(), writer); err != nil {
				return MergeResult{}, err
			}
		}
	}
	var result MergeResult
	err := uow.Do(ctx, request.Project, request.Suite, func(ctx context.Context, tx Tx) error {
		stored, found, err := tx.MergeOutcome(ctx, request.Carrier)
		if err != nil {
			return fmt.Errorf("load merge outcome: %w", err)
		}
		if found { // the request is not compared: a pull request merges once
			result = MergeResult{MergeOutcome: stored, Duplicate: true}
			return nil
		}
		outcome, err := decideMerge(ctx, tx, request, refusal, inv, integrated)
		if err != nil {
			return err
		}
		if err := tx.RecordMergeOutcome(ctx, MergeWrite{Carrier: request.Carrier, Outcome: outcome, RecordedAt: request.RecordedAt}); err != nil {
			return fmt.Errorf("record merge outcome: %w", err)
		}
		message, send, err := request.Message(outcome)
		if err != nil {
			return fmt.Errorf("build the outcome message: %w", err)
		}
		if send {
			if err := writeOutbox(ctx, tx, []OutboxMessage{message}); err != nil {
				return err
			}
		}
		result = MergeResult{MergeOutcome: outcome}
		return nil
	})
	if err != nil {
		return MergeResult{}, err
	}
	return result, nil
}

// decideMerge decides the outcome in the order of the contract and writes the assessment and the promotion it leads
// to. The caller records the outcome after them, because the foreign keys of the outcome are immediate.
func decideMerge(ctx context.Context, tx Tx, request MergeRequest, refusal MergeReason, inv inventory.Inventory, integrated contract.ProtectedContract) (MergeOutcome, error) {
	state, err := tx.Suite(ctx)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("load suite: %w", err)
	}
	current, present := state.Canonical.Suite().CurrentVersionID()
	if !present {
		return decideFirstMerge(ctx, tx, request, refusal, inv, integrated, state)
	}
	kept := MergeOutcome{Source: request.Source, Version: current}
	if request.Target != state.Target {
		kept.Kind = MergeNotTarget
		return kept, nil
	}
	first, found, err := tx.Version(ctx, "V1")
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("load the first version: %w", err)
	}
	if !found {
		return MergeOutcome{}, fmt.Errorf("%w: the suite has a canonical %q and no version V1", ErrInvalidState, current)
	}
	fromBranch, err := originatesFromBranch(ctx, tx, first)
	if err != nil {
		return MergeOutcome{}, err
	}
	if fromBranch && request.ObservedAt.Before(first.Record().RecordedAt()) { // part of, or before, the bootstrap baseline
		kept.Kind = MergeBeforeCanonical
		return kept, nil
	}
	if refusal != "" {
		return blockedMerge(kept, refusal), nil
	}
	change, err := contract.ClassifyContractChange(state.Canonical, integrated)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("classify the merge commit: %w", err)
	}
	if change == contract.ContractUnchanged {
		kept.Kind = MergeNoChange
		return kept, nil
	}
	id, found, err := tx.ChangeProposalFor(ctx, request.Carrier)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("find the change proposal of %q: %w", request.Carrier, err)
	}
	if !found {
		return blockedMerge(kept, MergeReasonUnapprovedChange), nil
	}
	proposal, _, err := tx.Proposal(ctx, id)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("load proposal: %w", err)
	}
	binding := proposal.Current().Binding()
	reference := binding.Reference()
	kept.Reference = &reference
	evidence, err := HeadEvidence(inv, binding, request.Source)
	if err != nil {
		return MergeOutcome{}, err
	}
	switch err := tx.RecordAssessment(ctx, AssessmentWrite{Reference: reference, Source: request.Source, Evidence: evidence}); {
	case errors.Is(err, ErrConflict): // deterministic, so failing the call would stall the later merges of the repository
		return blockedMerge(kept, MergeReasonAssessmentConflict), nil
	case err != nil:
		return MergeOutcome{}, fmt.Errorf("record the assessment of the merge commit: %w", err)
	}
	next, corrects, err := nextMergeVersion(ctx, tx, current, integrated)
	if err != nil {
		return MergeOutcome{}, err
	}
	integration, err := contract.NewIntegration(request.Project, request.Target, request.Source, request.Carrier, contract.IntegrationMergedChange)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	identity := PromotionIdentity{Kind: OperationPromote, CorrectsVersionID: corrects, Request: PromoteRequest{
		OperationID: contract.OperationID("merge:" + string(request.Carrier)), Reference: reference, Carrier: request.Carrier, Proposed: integrated,
		AssessmentSource: request.Source, Integration: integration, NewVersionID: next, RecordedAt: request.RecordedAt}}
	if corrects != "" {
		identity.Kind = OperationCorrect
	}
	promoted, err := promoteWithin(ctx, tx, identity)
	if err != nil {
		return MergeOutcome{}, err
	}
	switch promoted.Outcome {
	case contract.PromotionProposed:
		kept.Kind, kept.Version, kept.Corrects = MergePromoted, next, corrects
		if corrects != "" {
			kept.Kind = MergeCorrected
		}
		return kept, nil
	case contract.PromotionBlocked:
		if reason, known := promotionReasons[promoted.Reason]; known {
			return blockedMerge(kept, reason), nil
		}
	}
	return MergeOutcome{}, fmt.Errorf("%w: a merge decision with outcome %d and reason %d", ErrInvalidState, promoted.Outcome, promoted.Reason)
}

// originatesFromBranch reports whether the proposal revision that made version V1 canonical was sourced from a pinned
// default-branch commit. Only then does the time rule hold: the merges before such a baseline are part of it. A V1
// decided by a merge saw every earlier merge in order, so a PR merged after it is judged on its content.
func originatesFromBranch(ctx context.Context, tx Tx, first contract.HistoricalCanonical) (bool, error) {
	reference := first.Record().Binding().Reference()
	proposal, _, err := tx.Proposal(ctx, reference.ProposalID)
	if err != nil {
		return false, fmt.Errorf("load the proposal of the first version: %w", err)
	}
	for _, revision := range proposal.Revisions() {
		if revision.Binding().Reference() == reference {
			return revision.OriginKind() == contract.OriginBranch, nil
		}
	}
	return false, fmt.Errorf("%w: the first version names revision %v, which its proposal does not have", ErrInvalidState, reference)
}

// decideFirstMerge decides a merge of a Suite without a canonical. The merge of the bootstrap PR decides V1 (the carrier's
// bootstrap proposal, assessed at the merge commit and promoted with an IntegrationMergedChange); every other merge is
// before the canonical. None of them names a version, because there is none. The target is checked first, as with a
// canonical, so a merge into another branch is never the bootstrap.
func decideFirstMerge(ctx context.Context, tx Tx, request MergeRequest, refusal MergeReason, inv inventory.Inventory, integrated contract.ProtectedContract, state SuiteState) (MergeOutcome, error) {
	if refusal == MergeReasonIntegrityFailed { // it names a stored canonical, and there is none
		return MergeOutcome{}, fmt.Errorf("%w: %q is a refusal only while a canonical exists", ErrInvalidRequest, refusal)
	}
	kept := MergeOutcome{Source: request.Source, Kind: MergeBeforeCanonical}
	if request.Target != state.Target {
		return kept, nil
	}
	id, found, err := tx.BaselineProposalFor(ctx, request.Carrier)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("find the bootstrap proposal of %q: %w", request.Carrier, err)
	}
	if !found {
		return kept, nil
	}
	proposal, _, err := tx.Proposal(ctx, id)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("load proposal: %w", err)
	}
	binding := proposal.Current().Binding()
	reference := binding.Reference()
	kept.Reference = &reference
	if refusal != "" {
		return blockedMerge(kept, refusal), nil
	}
	evidence, err := HeadEvidence(inv, binding, request.Source)
	if err != nil {
		return MergeOutcome{}, err
	}
	switch err := tx.RecordAssessment(ctx, AssessmentWrite{Reference: reference, Source: request.Source, Evidence: evidence}); {
	case errors.Is(err, ErrConflict):
		return blockedMerge(kept, MergeReasonAssessmentConflict), nil
	case err != nil:
		return MergeOutcome{}, fmt.Errorf("record the assessment of the merge commit: %w", err)
	}
	integration, err := contract.NewIntegration(request.Project, request.Target, request.Source, request.Carrier, contract.IntegrationMergedChange)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	promoted, err := promoteWithin(ctx, tx, PromotionIdentity{Kind: OperationBootstrap, Request: PromoteRequest{
		OperationID: contract.OperationID("merge:" + string(request.Carrier)), Reference: reference, Carrier: request.Carrier, Proposed: integrated,
		AssessmentSource: request.Source, Integration: integration, NewVersionID: "V1", RecordedAt: request.RecordedAt}})
	if err != nil {
		return MergeOutcome{}, err
	}
	switch promoted.Outcome {
	case contract.PromotionProposed:
		kept.Kind, kept.Version = MergePromoted, "V1"
		return kept, nil
	case contract.PromotionBlocked:
		if reason, known := promotionReasons[promoted.Reason]; known {
			return blockedMerge(kept, reason), nil
		}
	}
	// CanonicalPresent and EmptyInventory cannot hold under the Suite lock with a bootstrap proposal of the merged tree.
	return MergeOutcome{}, fmt.Errorf("%w: a bootstrap merge decision with outcome %d and reason %d", ErrInvalidState, promoted.Outcome, promoted.Reason)
}

func blockedMerge(kept MergeOutcome, reason MergeReason) MergeOutcome {
	kept.Kind, kept.Reason = MergeBlocked, reason
	return kept
}

// nextMergeVersion allocates V<n+1> after the current V<n>. When an earlier version holds the integrated manifest,
// the merge restores it and the version corrected is the current one.
func nextMergeVersion(ctx context.Context, tx Tx, current contract.SuiteVersionID, integrated contract.ProtectedContract) (next, corrects contract.SuiteVersionID, err error) {
	digits, ok := strings.CutPrefix(string(current), "V")
	n, parseErr := strconv.ParseUint(digits, 10, 64)
	if !ok || parseErr != nil || n == 0 || strconv.FormatUint(n, 10) != digits {
		return "", "", fmt.Errorf("%w: the current version %q is not V<n>", ErrInvalidState, current)
	}
	for i := uint64(1); i < n; i++ {
		id := contract.SuiteVersionID("V" + strconv.FormatUint(i, 10))
		earlier, found, err := tx.Version(ctx, id)
		if err != nil {
			return "", "", fmt.Errorf("load version %q: %w", id, err)
		}
		if !found {
			return "", "", fmt.Errorf("%w: version %q is missing below the current %q", ErrInvalidState, id, current)
		}
		if earlier.Version().Manifest().Digest() == integrated.Manifest().Digest() {
			corrects = current
			break
		}
	}
	return contract.SuiteVersionID("V" + strconv.FormatUint(n+1, 10)), corrects, nil
}

// MergeOutcomeOf reads the outcome recorded for carrier, so a job can skip what is done before it reads a tree.
func MergeOutcomeOf(ctx context.Context, uow UnitOfWork, project contract.ProjectID, suite contract.SuiteID, carrier contract.ApprovalCarrierID) (MergeOutcome, bool, error) {
	if uow == nil || blank(string(project)) || blank(string(suite)) || blank(string(carrier)) {
		return MergeOutcome{}, false, fmt.Errorf("%w: a merge outcome is read by project, suite and carrier", ErrInvalidRequest)
	}
	var outcome MergeOutcome
	var found bool
	err := uow.Do(ctx, project, suite, func(ctx context.Context, tx Tx) (err error) {
		outcome, found, err = tx.MergeOutcome(ctx, carrier)
		return err
	})
	if err != nil {
		return MergeOutcome{}, false, err
	}
	return outcome, found, nil
}
