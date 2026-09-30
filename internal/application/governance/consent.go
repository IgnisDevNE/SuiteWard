package governance

import "context"

// ProcessConsent coordinates a domain command with its atomic stored outcome.
func ProcessConsent(ctx context.Context, store Store, request ConsentRequest) (ConsentResponse, error) {
	return ConsentResponse{}, nil
}
