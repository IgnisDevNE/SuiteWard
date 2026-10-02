package governance

import (
	"context"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Bootstrap establishes the first canonical through stored authority.
func Bootstrap(ctx context.Context, store Store, request BootstrapRequest) (PromoteResult, error) {
	if request.Mode != contract.ExistingBaselineBootstrap && request.Mode != contract.FirstTestBootstrap {
		return PromoteResult{}, ErrInvalidRequest
	}
	return runPromotion(ctx, store, PromotionIdentity{Kind: OperationBootstrap, Request: request.Promotion, BootstrapMode: request.Mode})
}

// Correct proposes a fresh canonical with attribution to a stored version.
func Correct(ctx context.Context, store Store, request CorrectionRequest) (PromoteResult, error) {
	if strings.TrimSpace(string(request.TargetVersionID)) == "" {
		return PromoteResult{}, ErrInvalidRequest
	}
	return runPromotion(ctx, store, PromotionIdentity{Kind: OperationCorrect, Request: request.Promotion, CorrectsVersionID: request.TargetVersionID})
}
