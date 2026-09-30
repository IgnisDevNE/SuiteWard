package contract

import "errors"

var ErrInvalidBootstrap = errors.New("invalid bootstrap")

type BootstrapMode uint8

const (
	ExistingBaselineBootstrap BootstrapMode = iota + 1
	FirstTestBootstrap
)

type BootstrapInput struct {
	Mode      BootstrapMode
	Promotion PromotionInput
}

func DecideBootstrap(input BootstrapInput) (PromotionDecision, error) {
	context := input.Promotion.Context
	return CheckPromotionReadiness(context, context.Proposal.Current().Origin())
}
