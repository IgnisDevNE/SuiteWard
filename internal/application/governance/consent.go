package governance

import (
	"context"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ProcessConsent coordinates a domain command with its atomic stored outcome.
func ProcessConsent(ctx context.Context, store Store, request ConsentRequest) (ConsentResponse, error) {
	command := request.Command
	if store == nil || command.OperationID() == "" {
		return ConsentResponse{}, ErrInvalidRequest
	}
	snapshot, err := store.Load(ctx, ReadRequest{Reference: command.Reference(), OperationID: command.OperationID(), SourceCommandID: command.SourceCommandID()})
	if err != nil {
		return ConsentResponse{}, err
	}
	if err := checkConsentIndexes(snapshot, command); err != nil {
		return ConsentResponse{}, err
	}
	if snapshot.Operation.Kind != 0 {
		return ConsentResponse{Receipt: snapshot.Operation.Consent, Committed: true, Duplicate: true}, nil
	}
	if !validConsentSnapshot(snapshot, command.Reference()) {
		return ConsentResponse{}, ErrInvalidSnapshot
	}
	next, result, err := snapshot.Consent.Apply(snapshot.Proposal, snapshot.Policy, command)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if result.Duplicate() {
		original := snapshot.Source.Consent
		if snapshot.Source.Kind != OperationConsent || original.Result.Duplicate() ||
			original.Result.Command() != result.Command() || original.Result.Outcome() != result.Outcome() || original.Result.Reason() != result.Reason() {
			return ConsentResponse{}, ErrInvalidSnapshot
		}
		if err := store.CommitConsent(ctx, snapshot.Fence, ConsentWrite{Command: command, Consent: next, Receipt: original, Alias: true}); err != nil {
			return ConsentResponse{}, err
		}
		return ConsentResponse{Receipt: original, Committed: true, Duplicate: true}, nil
	}
	if snapshot.Source.Kind != 0 || result.Reason() == contract.ConsentReasonCommandConflict {
		return ConsentResponse{}, ErrInvalidSnapshot
	}
	var promoted contract.SuiteVersionID
	if record := snapshot.HistoricalPromotion; !record.IsZero() {
		revision, err := snapshot.Proposal.Lookup(record.Binding().Reference(), record.Carrier())
		if err != nil || record.Binding().Reference() != command.Reference() || !revision.Binding().Equal(record.Binding()) {
			return ConsentResponse{}, ErrInvalidSnapshot
		}
		promoted = record.VersionID()
	}
	receipt := ConsentReceipt{
		Result: result, EvaluatedReference: snapshot.Proposal.Current().Binding().Reference(),
		PolicyRevisionID: snapshot.Policy.RevisionID(), CurrentApprovalEligible: next.HasApproval(snapshot.Proposal, snapshot.Policy),
		PromotedVersionID: promoted,
	}
	if err := store.CommitConsent(ctx, snapshot.Fence, ConsentWrite{Command: command, Consent: next, Receipt: receipt}); err != nil {
		return ConsentResponse{}, err
	}
	return ConsentResponse{Receipt: receipt, Committed: true}, nil
}

// Indexed identities are checked before evaluating any current authority.
func checkConsentIndexes(snapshot Snapshot, command contract.Command) error {
	for _, receipt := range []OperationReceipt{snapshot.Operation, snapshot.Source} {
		switch receipt.Kind {
		case 0:
			if receipt.Consent != (ConsentReceipt{}) || !emptyConsentIndexPromotion(receipt.Promotion) {
				return ErrInvalidSnapshot
			}
		case OperationConsent:
			if !emptyConsentIndexPromotion(receipt.Promotion) || !validConsentReceipt(receipt.Consent) {
				return ErrInvalidSnapshot
			}
		case OperationPromote, OperationBootstrap, OperationCorrect:
			if receipt.Consent != (ConsentReceipt{}) || receipt.Promotion.Identity.Kind != receipt.Kind || receipt.Promotion.Decision.Outcome() != contract.PromotionProposed {
				return ErrInvalidSnapshot
			}
		default:
			return ErrInvalidSnapshot
		}
	}
	if snapshot.Source.Kind != 0 && snapshot.Source.Kind != OperationConsent {
		return ErrInvalidSnapshot
	}
	for _, receipt := range []OperationReceipt{snapshot.Operation, snapshot.Source} {
		if receipt.Kind == 0 {
			continue
		}
		if receipt.Kind != OperationConsent {
			return ErrOperationConflict
		}
		original := receipt.Consent.Result.Command()
		if original.SourceCommandID() != command.SourceCommandID() || original.Actor() != command.Actor() || !sameConsentAggregate(original.Reference(), command.Reference()) {
			return ErrOperationConflict
		}
	}
	if snapshot.Operation.Kind != 0 && (snapshot.Source.Kind == 0 || snapshot.Operation.Consent != snapshot.Source.Consent) {
		return ErrInvalidSnapshot
	}
	return nil
}

func validConsentReceipt(receipt ConsentReceipt) bool {
	result := receipt.Result
	return result.Command().OperationID() != "" && result.Outcome() >= contract.ConsentApproved && result.Outcome() <= contract.ConsentRejected &&
		!result.Duplicate() && result.Reason() != contract.ConsentReasonCommandConflict && sameConsentAggregate(result.Command().Reference(), receipt.EvaluatedReference) &&
		strings.TrimSpace(string(receipt.EvaluatedReference.RevisionID)) != "" && strings.TrimSpace(string(receipt.PolicyRevisionID)) != "" &&
		(receipt.PromotedVersionID == "" || strings.TrimSpace(string(receipt.PromotedVersionID)) != "")
}

func sameConsentAggregate(a, b contract.ProposalReference) bool {
	return a.ProjectID == b.ProjectID && a.SuiteID == b.SuiteID && a.ProposalID == b.ProposalID
}

func emptyConsentIndexPromotion(receipt PromotionReceipt) bool {
	identity := receipt.Identity
	request := identity.Request
	return identity.Kind == 0 && identity.CorrectsVersionID == "" && identity.Binding.IsZero() &&
		receipt.Decision.Outcome() == 0 && request.OperationID == "" && request.Reference == (contract.ProposalReference{}) && request.Carrier == "" &&
		request.Proposed.IsZero() && request.AssessmentSource == "" && request.Integration.IsZero() && request.NewVersionID == "" && request.RecordedAt.IsZero()
}

func validConsentSnapshot(snapshot Snapshot, reference contract.ProposalReference) bool {
	if snapshot.Canonical.IsZero() || snapshot.Proposal.IsZero() {
		return false
	}
	suite := snapshot.Canonical.Suite()
	current := snapshot.Proposal.Current().Binding().Reference()
	return suite.ProjectID() == reference.ProjectID && suite.ID() == reference.SuiteID &&
		snapshot.Fence == (AuthorityFence{ProjectID: suite.ProjectID(), SuiteID: suite.ID(), Revision: suite.Revision()}) &&
		current.ProjectID == reference.ProjectID && current.SuiteID == reference.SuiteID && current.ProposalID == reference.ProposalID &&
		snapshot.Policy.ProjectID() == reference.ProjectID
}
