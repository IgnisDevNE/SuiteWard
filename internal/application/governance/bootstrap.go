package governance

import (
	"context"
	"fmt"
)

// Bootstrap establishes the first canonical from an integrated, existing baseline: it replays by operation id,
// otherwise decides in the domain and writes only a proposed promotion with the request's outbox.
func Bootstrap(ctx context.Context, uow UnitOfWork, request PromoteRequest) (PromoteResult, error) {
	if uow == nil || !validPromoteRequest(request) {
		return PromoteResult{}, fmt.Errorf("%w: a bootstrap needs a unit of work and a complete request", ErrInvalidRequest)
	}
	identity := PromotionIdentity{Kind: OperationBootstrap, Request: request}
	var result PromoteResult
	err := uow.Do(ctx, request.Reference.ProjectID, request.Reference.SuiteID, func(ctx context.Context, tx Tx) error {
		stored, found, err := tx.Receipt(ctx, request.OperationID)
		if err != nil {
			return fmt.Errorf("load operation receipt: %w", err)
		}
		if found {
			result, err = replayPromotion(stored, identity)
			return err
		}
		result, err = promoteWithin(ctx, tx, identity)
		return err
	})
	if err != nil {
		return PromoteResult{}, err
	}
	return result, nil
}
