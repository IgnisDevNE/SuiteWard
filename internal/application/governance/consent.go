package governance

import (
	"context"
	"errors"
)

var errNotImplemented = errors.New("not implemented")

// ProcessConsent coordinates a domain command with its atomic stored outcome.
func ProcessConsent(ctx context.Context, uow UnitOfWork, request ConsentRequest) (ConsentResponse, error) {
	return ConsentResponse{}, errNotImplemented
}
