package installer

import (
	"context"
	"errors"
)

// Restore is `suiteward restore <backup.tar.age> [--kit PATH] [flags]`.
func Restore(_ context.Context, _ []string, _ Env) error {
	return errors.New("restore is not implemented")
}
