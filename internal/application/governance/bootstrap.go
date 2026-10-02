package governance

import "context"

// Bootstrap establishes the first canonical through stored authority.
func Bootstrap(ctx context.Context, store Store, request BootstrapRequest) (PromoteResult, error) {
	return PromoteResult{}, nil
}
