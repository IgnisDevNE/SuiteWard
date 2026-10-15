package contract

import "errors"

var ErrInvalidBootstrap = errors.New("invalid bootstrap")

// BootstrapInput establishes the first canonical from an integrated baseline.
type BootstrapInput struct {
	Promotion PromotionInput
}

// DecideBootstrap uses shared promotion guards and requires the integration that
// matches where the proposal's source comes from: an existing baseline for a
// branch revision, the merge of its carrier for a PR head. No pre-integration
// readiness can establish a canonical version. Integration facts are supplied.
func DecideBootstrap(input BootstrapInput) (PromotionDecision, error) {
	context := input.Promotion.Context
	if context.Canonical.IsZero() || context.Proposal.IsZero() || context.Proposed.IsZero() || input.Promotion.CorrectsVersionID != "" {
		return PromotionDecision{}, ErrInvalidBootstrap
	}
	if _, present := context.Canonical.Suite().CurrentVersionID(); present {
		return blockedPromotion(PromotionReasonCanonicalPresent), nil
	}
	if integration := input.Promotion.Integration; !integration.IsZero() && !integratesOrigin(integration.Kind(), context.Proposal.Current().OriginKind()) {
		return blockedPromotion(PromotionReasonIntegrationMismatch), nil
	}
	return DecidePromotion(input.Promotion)
}

// integratesOrigin pairs the two accepted shapes: a pinned branch commit is integrated as it is, a PR head only by its merge.
func integratesOrigin(integration IntegrationKind, origin OriginKind) bool {
	return integration == IntegrationExistingBaseline && origin == OriginBranch || integration == IntegrationMergedChange && origin == OriginPull
}
