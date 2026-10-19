package governance

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// promoteWithin decides a bootstrap, a merged change or a correction against the state tx holds and writes a proposed
// promotion with its outbox. It does not look for a replay: the caller owns the operation's identity. Bootstrap and
// PromoteMerge are its callers.
func promoteWithin(ctx context.Context, tx Tx, identity PromotionIdentity) (PromoteResult, error) {
	request := identity.Request
	state, err := tx.Suite(ctx)
	if err != nil {
		return PromoteResult{}, fmt.Errorf("load suite: %w", err)
	}
	proposal, consent, err := tx.Proposal(ctx, request.Reference.ProposalID)
	if err != nil {
		return PromoteResult{}, fmt.Errorf("load proposal: %w", err)
	}
	assessment, _, err := tx.Assessment(ctx, request.Reference, request.AssessmentSource) // missing evidence blocks the promotion
	if err != nil {
		return PromoteResult{}, fmt.Errorf("load assessment: %w", err)
	}
	input := contract.PromotionInput{
		Context: contract.PromotionContext{
			Canonical: state.Canonical, Proposed: request.Proposed, Proposal: proposal,
			Reference: request.Reference, Carrier: request.Carrier, Policy: state.Policy, Consent: consent,
			Assessment: assessment,
		},
		Integration: request.Integration, Target: state.Target, OperationID: request.OperationID,
		NewVersionID: request.NewVersionID, RecordedAt: request.RecordedAt, CorrectsVersionID: identity.CorrectsVersionID,
	}
	var decision contract.PromotionDecision
	switch identity.Kind {
	case OperationBootstrap:
		decision, err = contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
	case OperationCorrect:
		// PromoteMerge names the current version, which the history holds; a version that is not found is the zero
		// value, which DecideCorrection refuses (ErrInvalidCorrection), so it is not checked twice.
		var target contract.HistoricalCanonical
		target, _, err = tx.Version(ctx, identity.CorrectsVersionID)
		if err != nil {
			return PromoteResult{}, fmt.Errorf("load corrected version: %w", err)
		}
		decision, err = contract.DecideCorrection(contract.CorrectionInput{Promotion: input, Target: target})
	case OperationPromote:
		decision, err = contract.DecidePromotion(input)
	default: // Bootstrap and PromoteMerge name no other kind
		return PromoteResult{}, fmt.Errorf("%w: %q is not a promotion kind", ErrInvalidState, identity.Kind)
	}
	if err != nil {
		return PromoteResult{}, fmt.Errorf("decide %s: %w", identity.Kind, err)
	}
	result := PromoteResult{Outcome: decision.Outcome(), Reason: decision.Reason()}
	if decision.Outcome() != contract.PromotionProposed {
		return result, nil
	}
	effect, proposed := decision.Effect()
	if !proposed {
		return PromoteResult{}, fmt.Errorf("%w: promotion proposed without an effect", ErrInvalidState)
	}
	identity.Binding = proposal.Current().Binding()
	identity.Request.Outbox = nil // the receipt records the request, not what the caller publishes about it
	write := PromotionWrite{Receipt: PromotionReceipt{Identity: identity, Record: effect.Promotion()}, Version: effect.Version()}
	if err := tx.RecordPromotion(ctx, write); err != nil {
		return PromoteResult{}, fmt.Errorf("record promotion: %w", err)
	}
	if err := writeOutbox(ctx, tx, request.Outbox); err != nil {
		return PromoteResult{}, err
	}
	return result, nil
}

// replayPromotion answers a Bootstrap from the stored receipt alone: the same request returns the original outcome; any
// other use of the operation id conflicts, including its use by a consent command or a merge, whose receipts are of
// another kind.
func replayPromotion(stored OperationReceipt, wanted PromotionIdentity) (PromoteResult, error) {
	reference := wanted.Request.Reference
	if stored.Kind != wanted.Kind || stored.ProjectID != reference.ProjectID || stored.SuiteID != reference.SuiteID {
		return PromoteResult{}, fmt.Errorf("%w: the operation id belongs to another kind of operation or another suite", ErrOperationConflict)
	}
	if stored.Promotion == nil {
		return PromoteResult{}, fmt.Errorf("%w: %s receipt without a promotion", ErrInvalidState, stored.Kind)
	}
	if !samePromotionIdentity(stored.Promotion.Identity, wanted) {
		return PromoteResult{}, fmt.Errorf("%w: the operation id was used for another bootstrap request", ErrOperationConflict)
	}
	return PromoteResult{Outcome: contract.PromotionProposed, Duplicate: true}, nil
}

// samePromotionIdentity compares the requests of two bootstraps, which correct nothing.
func samePromotionIdentity(left, right PromotionIdentity) bool {
	a, b := left.Request, right.Request
	return left.Kind == right.Kind && a.OperationID == b.OperationID && a.Reference == b.Reference && a.Carrier == b.Carrier && a.Proposed.Equal(b.Proposed) &&
		a.AssessmentSource == b.AssessmentSource && a.Integration == b.Integration && a.NewVersionID == b.NewVersionID
}

func validPromoteRequest(request PromoteRequest) bool {
	r := request.Reference
	return !blank(string(request.OperationID)) && !blank(string(r.ProjectID)) && !blank(string(r.SuiteID)) &&
		!blank(string(r.ProposalID)) && !blank(string(r.RevisionID)) && !blank(string(request.Carrier)) &&
		!request.Proposed.IsZero() && !blank(string(request.AssessmentSource))
}
