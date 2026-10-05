package governance

import "context"

// Promote coordinates a domain decision with the stored authority boundary.
func Promote(ctx context.Context, uow UnitOfWork, request PromoteRequest) (PromoteResult, error) {
	return PromoteResult{}, errNotImplemented
}
