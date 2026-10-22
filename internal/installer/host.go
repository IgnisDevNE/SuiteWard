package installer

import (
	"context"
	"errors"
	"io"
)

// PrepareHost is `sudo suiteward install host`.
func PrepareHost(ctx context.Context, r *Resolver, d Deps, out io.Writer) error {
	return errors.New("not implemented")
}
