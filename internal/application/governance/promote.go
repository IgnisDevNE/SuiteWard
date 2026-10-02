package governance

import (
	"context"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Promote coordinates a domain decision with the stored authority boundary.
func Promote(ctx context.Context, store Store, request PromoteRequest) (PromoteResult, error) {
	if !validPromoteRequest(request) {
		return PromoteResult{}, ErrInvalidRequest
	}
	snapshot, err := store.Load(ctx, ReadRequest{Reference: request.Reference, OperationID: request.OperationID, AssessmentSource: request.AssessmentSource})
	if err != nil {
		return PromoteResult{}, err
	}
	if !validPromotionSnapshot(snapshot, request.Reference) {
		return PromoteResult{}, ErrInvalidSnapshot
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

func validPromoteRequest(request PromoteRequest) bool {
	r := request.Reference
	return strings.TrimSpace(string(request.OperationID)) != "" && strings.TrimSpace(string(r.ProjectID)) != "" && strings.TrimSpace(string(r.SuiteID)) != "" &&
		strings.TrimSpace(string(r.ProposalID)) != "" && strings.TrimSpace(string(r.RevisionID)) != "" && strings.TrimSpace(string(request.Carrier)) != "" &&
		!request.Proposed.IsZero() && strings.TrimSpace(string(request.AssessmentSource)) != ""
}

func validPromotionSnapshot(snapshot Snapshot, reference contract.ProposalReference) bool {
	if snapshot.Canonical.IsZero() || snapshot.Proposal.IsZero() || snapshot.Policy.RevisionID() == "" || snapshot.Scheduling.IsZero() || strings.TrimSpace(string(snapshot.Target)) == "" {
		return false
	}
	suite := snapshot.Canonical.Suite()
	stored := snapshot.Proposal.Current().Binding().Reference()
	return snapshot.Fence == (AuthorityFence{suite.ProjectID(), suite.ID(), suite.Revision()}) && suite.ProjectID() == reference.ProjectID && suite.ID() == reference.SuiteID &&
		stored.ProjectID == reference.ProjectID && stored.SuiteID == reference.SuiteID && stored.ProposalID == reference.ProposalID &&
		snapshot.Policy.ProjectID() == reference.ProjectID && snapshot.Scheduling.ProjectID() == reference.ProjectID && snapshot.Scheduling.SuiteID() == reference.SuiteID
}
