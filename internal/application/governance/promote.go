package governance

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Promote coordinates a domain decision with the stored authority boundary.
func Promote(ctx context.Context, store Store, request PromoteRequest) (PromoteResult, error) {
	snapshot, err := store.Load(ctx, ReadRequest{Reference: request.Reference, OperationID: request.OperationID, AssessmentSource: request.AssessmentSource})
	if err != nil {
		return PromoteResult{}, err
	}
	decision, err := contract.DecidePromotion(contract.PromotionInput{
		Context: contract.PromotionContext{
			Canonical: snapshot.Canonical, Proposed: request.Proposed, Proposal: snapshot.Proposal,
			Reference: request.Reference, Carrier: request.Carrier, Policy: snapshot.Policy, Consent: snapshot.Consent,
			Assessment: snapshot.Assessment, Scheduling: snapshot.Scheduling,
			ExpectedStateRevision: snapshot.Fence.Revision, ExpectedSchedulingGeneration: snapshot.Scheduling.Generation(),
		},
		Integration: request.Integration, Target: snapshot.Target, OperationID: request.OperationID,
		NewVersionID: request.NewVersionID, RecordedAt: request.RecordedAt,
	})
	if err != nil {
		return PromoteResult{}, err
	}
	if decision.Outcome() != contract.PromotionProposed {
		return PromoteResult{Decision: decision}, nil
	}
	scheduling, err := snapshot.Scheduling.Observe(snapshot.Proposal, snapshot.Scheduling.Generation(), contract.ObservePromoted)
	if err != nil {
		return PromoteResult{}, err
	}
	receipt := PromotionReceipt{Identity: PromotionIdentity{Kind: OperationPromote, Request: request, Binding: snapshot.Proposal.Current().Binding()}, Decision: decision}
	if err := store.CommitPromotion(ctx, snapshot.Fence, PromotionWrite{Receipt: receipt, Scheduling: scheduling}); err != nil {
		return PromoteResult{}, err
	}
	return PromoteResult{Decision: decision, Committed: true}, nil
}
