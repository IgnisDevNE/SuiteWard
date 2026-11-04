//go:build !windows

package app

import (
	"context"
	"log/slog"
)

// watchConsoleHost does nothing here: only the scheduled task of Windows leaves the executor without its console host when it is stopped. It returns
// a context that the caller ends, like the one of the Windows watch.
func watchConsoleHost(ctx context.Context, _ *slog.Logger) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}
