package governance

import (
	"context"
	"strings"
)

// Bootstrap establishes the first canonical from an integrated, existing baseline.
func Bootstrap(ctx context.Context, uow UnitOfWork, request PromoteRequest) (PromoteResult, error) {
	return runPromotion(ctx, uow, PromotionIdentity{Kind: OperationBootstrap, Request: request})
}

// Correct proposes a fresh canonical with attribution to a stored version.
func Correct(ctx context.Context, uow UnitOfWork, request CorrectionRequest) (PromoteResult, error) {
	if strings.TrimSpace(string(request.TargetVersionID)) == "" {
		return PromoteResult{}, ErrInvalidRequest
	}
	return runPromotion(ctx, uow, PromotionIdentity{Kind: OperationCorrect, Request: request.Promotion, CorrectsVersionID: request.TargetVersionID})
}
