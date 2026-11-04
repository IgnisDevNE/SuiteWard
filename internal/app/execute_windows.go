//go:build windows

package app

import (
	"context"
	"log/slog"
)

// processEntry is a process of the computer as the snapshot lists it: the process that started it, and the name of its image.
type processEntry struct {
	parent uint32
	name   string
}

// consoleHostOf is the process id of the console host that started the loop of run.cmd, when self runs in it.
func consoleHostOf(self uint32, procs map[uint32]processEntry) (uint32, bool) {
	return 0, false
}

// watchConsoleHost ends the run when the console host of the executor's loop is gone.
func watchConsoleHost(ctx context.Context, log *slog.Logger) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}
