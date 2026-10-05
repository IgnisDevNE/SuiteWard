package governance

import "context"

// Bootstrap establishes the first canonical from an integrated baseline.
func Bootstrap(ctx context.Context, uow UnitOfWork, request PromoteRequest) (PromoteResult, error) {
	return PromoteResult{}, errNotImplemented
}

// Correct proposes a fresh canonical with attribution to a stored version.
func Correct(ctx context.Context, uow UnitOfWork, request CorrectionRequest) (PromoteResult, error) {
	return PromoteResult{}, errNotImplemented
}
