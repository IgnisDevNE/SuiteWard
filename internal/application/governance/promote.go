package governance

import (
	"context"
	"maps"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Promote coordinates a domain decision with the stored authority boundary.
func Promote(ctx context.Context, store Store, request PromoteRequest) (PromoteResult, error) {
	return runPromotion(ctx, store, PromotionIdentity{Kind: OperationPromote, Request: request})
}

func runPromotion(ctx context.Context, store Store, identity PromotionIdentity) (PromoteResult, error) {
	request := identity.Request
	if !validPromoteRequest(request) {
		return PromoteResult{}, ErrInvalidRequest
	}
	snapshot, err := store.Load(ctx, ReadRequest{Reference: request.Reference, OperationID: request.OperationID, AssessmentSource: request.AssessmentSource})
	if err != nil {
		return PromoteResult{}, err
	}
	if result, found, err := replayPromotion(snapshot.Operation, identity); found || err != nil {
		return result, err
	}
	if !validPromotionSnapshot(snapshot, request.Reference) {
		return PromoteResult{}, ErrInvalidSnapshot
	}
	input := contract.PromotionInput{
		Context: contract.PromotionContext{
			Canonical: snapshot.Canonical, Proposed: request.Proposed, Proposal: snapshot.Proposal,
			Reference: request.Reference, Carrier: request.Carrier, Policy: snapshot.Policy, Consent: snapshot.Consent,
			Assessment: snapshot.Assessment, Scheduling: snapshot.Scheduling,
			ExpectedStateRevision: snapshot.Fence.Revision, ExpectedSchedulingGeneration: snapshot.Scheduling.Generation(),
		},
		Integration: request.Integration, Target: snapshot.Target, OperationID: request.OperationID,
		NewVersionID: request.NewVersionID, RecordedAt: request.RecordedAt,
	}
	var decision contract.PromotionDecision
	if identity.Kind == OperationBootstrap {
		decision, err = contract.DecideBootstrap(contract.BootstrapInput{Mode: identity.BootstrapMode, Promotion: input})
	} else {
		decision, err = contract.DecidePromotion(input)
	}
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
	identity.Binding = snapshot.Proposal.Current().Binding()
	receipt := PromotionReceipt{Identity: identity, Decision: decision}
	if err := store.CommitPromotion(ctx, snapshot.Fence, PromotionWrite{Receipt: receipt, Scheduling: scheduling}); err != nil {
		return PromoteResult{}, err
	}
	return PromoteResult{Decision: decision, Committed: true}, nil
}

// Replay validates the original receipt against itself, never against the
// current proposal, policy, configured target, or canonical pointer.
func replayPromotion(stored OperationReceipt, wanted PromotionIdentity) (PromoteResult, bool, error) {
	if stored.Kind == 0 {
		return PromoteResult{}, false, nil
	}
	if stored.Kind < OperationPromote || stored.Kind > OperationConsent {
		return PromoteResult{}, false, ErrInvalidSnapshot
	}
	if stored.Kind == OperationConsent {
		return PromoteResult{}, false, ErrOperationConflict
	}
	receipt := stored.Promotion
	if stored.Kind != receipt.Identity.Kind || !validPromotionReceipt(receipt) {
		return PromoteResult{}, false, ErrInvalidSnapshot
	}
	if !samePromotionIdentity(receipt.Identity, wanted) {
		return PromoteResult{}, false, ErrOperationConflict
	}
	return PromoteResult{Decision: receipt.Decision, Committed: true, Duplicate: true}, true, nil
}

func samePromotionIdentity(left, right PromotionIdentity) bool {
	a, b := left.Request, right.Request
	return left.Kind == right.Kind && left.BootstrapMode == right.BootstrapMode && left.CorrectsVersionID == right.CorrectsVersionID &&
		a.OperationID == b.OperationID && a.Reference == b.Reference && a.Carrier == b.Carrier && a.Proposed.Equal(b.Proposed) &&
		a.AssessmentSource == b.AssessmentSource && a.Integration == b.Integration && a.NewVersionID == b.NewVersionID
}

func validPromotionReceipt(receipt PromotionReceipt) bool {
	id := receipt.Identity
	r := id.Request
	effect, present := receipt.Decision.Effect()
	if !validPromoteRequest(r) || !present || receipt.Decision.Outcome() != contract.PromotionProposed || id.Binding.IsZero() ||
		id.Binding.Reference() != r.Reference || id.Binding.ManifestDigest() != r.Proposed.Manifest().Digest() || id.Binding.ScopeDigest() != r.Proposed.ScopeDigest() ||
		!maps.Equal(id.Binding.CoveredInputs(), r.Proposed.CoveredInputs()) {
		return false
	}
	record := effect.Promotion()
	if !record.Binding().Equal(id.Binding) || record.OperationID() != r.OperationID || record.VersionID() != r.NewVersionID || record.Carrier() != r.Carrier ||
		record.Source() != r.AssessmentSource || record.Source() != r.Integration.Source() || record.Target() != r.Integration.Target() ||
		r.Integration.ProjectID() != r.Reference.ProjectID || !record.RecordedAt().Equal(r.RecordedAt) || record.CorrectsVersionID() != id.CorrectsVersionID {
		return false
	}
	switch id.Kind {
	case OperationPromote:
		return id.BootstrapMode == 0 && id.CorrectsVersionID == ""
	case OperationBootstrap:
		return id.CorrectsVersionID == "" && ((id.BootstrapMode == contract.ExistingBaselineBootstrap && r.Integration.Kind() == contract.IntegrationExistingBaseline) ||
			(id.BootstrapMode == contract.FirstTestBootstrap && r.Integration.Kind() == contract.IntegrationMergedChange))
	case OperationCorrect:
		return id.BootstrapMode == 0 && id.CorrectsVersionID != ""
	default:
		return false
	}
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
