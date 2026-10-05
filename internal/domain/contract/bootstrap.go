package contract

import "errors"

var ErrInvalidBootstrap = errors.New("invalid bootstrap")

// BootstrapInput establishes the first canonical from an integrated baseline.
type BootstrapInput struct {
	Promotion PromotionInput
}

// DecideBootstrap uses shared promotion guards and requires an existing
// baseline integration; no pre-integration readiness can establish a canonical
// version. Integration facts are supplied.
func DecideBootstrap(input BootstrapInput) (PromotionDecision, error) {
	context := input.Promotion.Context
	if context.Canonical.IsZero() || context.Proposal.IsZero() || context.Proposed.IsZero() || input.Promotion.CorrectsVersionID != "" {
		return PromotionDecision{}, ErrInvalidBootstrap
	}
	if _, present := context.Canonical.Suite().CurrentVersionID(); present {
		return blockedPromotion(PromotionReasonCanonicalPresent), nil
	}
	if !input.Promotion.Integration.IsZero() && input.Promotion.Integration.Kind() != IntegrationExistingBaseline {
		return blockedPromotion(PromotionReasonIntegrationMismatch), nil
	}
	return DecidePromotion(input.Promotion)
}
