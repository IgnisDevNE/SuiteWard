package governance

import (
	"context"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Promote decides and records a promotion after a merged change inside one
// unit of work.
func Promote(ctx context.Context, uow UnitOfWork, request PromoteRequest) (PromoteResult, error) {
	return runPromotion(ctx, uow, PromotionIdentity{Kind: OperationPromote, Request: request})
}

// runPromotion serves promotion, bootstrap and correction: replay by operation
// id, otherwise decide in the domain and write only a proposed promotion.
func runPromotion(ctx context.Context, uow UnitOfWork, identity PromotionIdentity) (PromoteResult, error) {
	request := identity.Request
	if uow == nil || !validPromoteRequest(request) {
		return PromoteResult{}, ErrInvalidRequest
	}
	var result PromoteResult
	err := uow.Do(ctx, request.Reference.ProjectID, request.Reference.SuiteID, func(ctx context.Context, tx Tx) error {
		stored, found, err := tx.Receipt(ctx, request.OperationID)
		if err != nil {
			return fmt.Errorf("load operation receipt: %w", err)
		}
		if found {
			result, err = replayPromotion(stored, identity)
			return err
		}
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		proposal, consent, err := tx.Proposal(ctx, request.Reference.ProposalID)
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		assessment, _, err := tx.Assessment(ctx, request.Reference, request.AssessmentSource) // missing evidence blocks the promotion
		if err != nil {
			return fmt.Errorf("load assessment: %w", err)
		}
		input := contract.PromotionInput{
			Context: contract.PromotionContext{
				Canonical: state.Canonical, Proposed: request.Proposed, Proposal: proposal,
				Reference: request.Reference, Carrier: request.Carrier, Policy: state.Policy, Consent: consent,
				Assessment: assessment, Scheduling: state.Schedule,
			},
			Integration: request.Integration, Target: state.Target, OperationID: request.OperationID,
			NewVersionID: request.NewVersionID, RecordedAt: request.RecordedAt, CorrectsVersionID: identity.CorrectsVersionID,
		}
		var decision contract.PromotionDecision
		switch identity.Kind {
		case OperationBootstrap:
			decision, err = contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
		case OperationCorrect:
			var target contract.HistoricalCanonical
			if target, found, err = tx.Version(ctx, identity.CorrectsVersionID); err != nil {
				return fmt.Errorf("load corrected version: %w", err)
			}
			if !found {
				return fmt.Errorf("%w: version %q", ErrNotFound, identity.CorrectsVersionID)
			}
			decision, err = contract.DecideCorrection(contract.CorrectionInput{Promotion: input, Target: target})
		default:
			decision, err = contract.DecidePromotion(input)
		}
		if err != nil {
			return fmt.Errorf("decide %s: %w", identity.Kind, err)
		}
		result = PromoteResult{Outcome: decision.Outcome(), Reason: decision.Reason()}
		effect, proposed := decision.Effect()
		if decision.Outcome() != contract.PromotionProposed || !proposed {
			return nil
		}
		schedule, err := state.Schedule.Observe(proposal, contract.ObservePromoted)
		if err != nil {
			return fmt.Errorf("observe promotion in schedule: %w", err)
		}
		identity.Binding = proposal.Current().Binding()
		write := PromotionWrite{Receipt: PromotionReceipt{Identity: identity, Record: effect.Promotion()}, Version: effect.Version(), Schedule: schedule}
		if err := tx.RecordPromotion(ctx, write); err != nil {
			return fmt.Errorf("record promotion: %w", err)
		}
		result.Record, result.Committed = effect.Promotion(), true
		return nil
	})
	if err != nil {
		return PromoteResult{}, err
	}
	return result, nil
}

// replayPromotion answers from the stored receipt alone: the same request
// returns the original record; any other use of the operation id conflicts.
func replayPromotion(stored OperationReceipt, wanted PromotionIdentity) (PromoteResult, error) {
	reference := wanted.Request.Reference
	if stored.Kind != wanted.Kind || stored.ProjectID != reference.ProjectID || stored.SuiteID != reference.SuiteID {
		return PromoteResult{}, ErrOperationConflict
	}
	if stored.Promotion == nil {
		return PromoteResult{}, fmt.Errorf("%w: %s receipt without a promotion", ErrInvalidState, stored.Kind)
	}
	if !samePromotionIdentity(stored.Promotion.Identity, wanted) {
		return PromoteResult{}, ErrOperationConflict
	}
	return PromoteResult{Outcome: contract.PromotionProposed, Record: stored.Promotion.Record, Committed: true, Duplicate: true}, nil
}

func samePromotionIdentity(left, right PromotionIdentity) bool {
	a, b := left.Request, right.Request
	return left.Kind == right.Kind && left.CorrectsVersionID == right.CorrectsVersionID &&
		a.OperationID == b.OperationID && a.Reference == b.Reference && a.Carrier == b.Carrier && a.Proposed.Equal(b.Proposed) &&
		a.AssessmentSource == b.AssessmentSource && a.Integration == b.Integration && a.NewVersionID == b.NewVersionID
}

func validPromoteRequest(request PromoteRequest) bool {
	r := request.Reference
	return strings.TrimSpace(string(request.OperationID)) != "" && strings.TrimSpace(string(r.ProjectID)) != "" && strings.TrimSpace(string(r.SuiteID)) != "" &&
		strings.TrimSpace(string(r.ProposalID)) != "" && strings.TrimSpace(string(r.RevisionID)) != "" && strings.TrimSpace(string(request.Carrier)) != "" &&
		!request.Proposed.IsZero() && strings.TrimSpace(string(request.AssessmentSource)) != ""
}
