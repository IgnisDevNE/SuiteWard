package app

import (
	"context"
	"io"
	"log/slog"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// newLogger is the structured JSON logger of the runtime contract.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// serve runs the service until ctx is canceled and returns the exit code:
// 0 for a clean stop, 1 for a fatal startup error or an unclean stop. handlers
// are job handlers added to the built-in probe.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, handlers map[string]river.Handler) int {
	return -1
}
