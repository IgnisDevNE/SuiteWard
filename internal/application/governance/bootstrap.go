package governance

import (
	"context"
	"strings"
)

// Bootstrap establishes the first canonical through stored authority.
func Bootstrap(ctx context.Context, store Store, request BootstrapRequest) (PromoteResult, error) {
	return runPromotion(ctx, store, PromotionIdentity{Kind: OperationBootstrap, Request: request.Promotion})
}

// Correct proposes a fresh canonical with attribution to a stored version.
func Correct(ctx context.Context, store Store, request CorrectionRequest) (PromoteResult, error) {
	if strings.TrimSpace(string(request.TargetVersionID)) == "" {
		return PromoteResult{}, ErrInvalidRequest
	}
	return runPromotion(ctx, store, PromotionIdentity{Kind: OperationCorrect, Request: request.Promotion, CorrectsVersionID: request.TargetVersionID})
}
