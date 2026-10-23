package installer

import (
	"context"
	"errors"
)

// Backup is `suiteward backup [--to DIR] [flags]`.
func Backup(_ context.Context, _ []string, _ Env) error {
	return errors.New("backup is not implemented")
}
