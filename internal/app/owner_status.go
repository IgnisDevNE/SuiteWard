package app

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// ownerReader is the part of the owner store /status and MCP status read.
type ownerReader interface {
	Current(ctx context.Context) (owner.State, bool, error)
}

// effectiveTier is the tier /status reports and the note that says why it may
// differ from the configured one (D-IDENTITY).
func effectiveTier(configured string, enrollment owner.State, enrolled bool) (tier, note string) {
	return configured, ""
}
