package river

import (
	"context"
	"encoding/json"
	"log/slog"
)

// ProbeKind is the kind of the diagnostic job and of its outbox message.
const ProbeKind = "probe"

// Handler runs one job of a kind. Jobs are delivered at least once, so a
// handler must be idempotent.
type Handler func(ctx context.Context, args json.RawMessage) error

// ProbeHandler only logs; it exists to verify the runtime through the real worker.
func ProbeHandler(logger *slog.Logger) Handler {
	return func(ctx context.Context, _ json.RawMessage) error {
		logger.InfoContext(ctx, "probe job ran")
		return nil
	}
}

// ProbePublisher only logs.
func ProbePublisher(logger *slog.Logger) Publisher {
	return func(ctx context.Context, key string, _ json.RawMessage) error {
		logger.InfoContext(ctx, "probe message published", "key", key)
		return nil
	}
}
