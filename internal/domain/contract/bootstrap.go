package contract

import "errors"

var ErrInvalidBootstrap = errors.New("invalid bootstrap")

// BootstrapMode separates an integrated baseline from a first-test change.
type BootstrapMode uint8

const (
	ExistingBaselineBootstrap BootstrapMode = iota + 1
	FirstTestBootstrap
)

type BootstrapInput struct {
	Mode      BootstrapMode
	Promotion PromotionInput
}

// DecideBootstrap uses shared promotion guards without allowing pre-integration
// readiness to establish a canonical version. Integration facts are supplied.
func DecideBootstrap(input BootstrapInput) (PromotionDecision, error) {
	context := input.Promotion.Context
	if (input.Mode != ExistingBaselineBootstrap && input.Mode != FirstTestBootstrap) || context.Canonical.IsZero() ||
		context.Proposal.IsZero() || context.Proposed.IsZero() || input.Promotion.CorrectsVersionID != "" {
		return PromotionDecision{}, ErrInvalidBootstrap
	}
	if _, present := context.Canonical.Suite().CurrentVersionID(); present {
		return blockedPromotion(PromotionReasonCanonicalPresent), nil
	}
	if input.Mode == FirstTestBootstrap && input.Promotion.Integration.IsZero() {
		return CheckPromotionReadiness(context, context.Proposal.Current().Origin())
	}
	if !input.Promotion.Integration.IsZero() &&
		((input.Mode == ExistingBaselineBootstrap && input.Promotion.Integration.Kind() != IntegrationExistingBaseline) ||
			(input.Mode == FirstTestBootstrap && input.Promotion.Integration.Kind() != IntegrationMergedChange)) {
		return blockedPromotion(PromotionReasonIntegrationMismatch), nil
	}
	return DecidePromotion(input.Promotion)
}
