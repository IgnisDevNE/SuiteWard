package installer

import (
	"context"
	"errors"
)

// BackupInit is `suiteward backup init [--kit PATH] [--yes] [--replace] [flags]`.
func BackupInit(_ context.Context, _ []string, _ Env) error {
	return errors.New("backup init is not implemented")
}
