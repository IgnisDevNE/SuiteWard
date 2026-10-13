package app

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// tierNote is what /status and MCP status say when the configured tier and the
// tier recorded at enrollment differ.
const tierNote = "The configured tier and the tier recorded at enrollment differ, so the effective tier is co-located until setup is run again."

// ownerReader is the part of the owner store /status and MCP status read.
type ownerReader interface {
	Current(ctx context.Context) (owner.State, bool, error)
}

// effectiveTier is the tier /status reports: co-located when either the
// configured tier or the enrollment says so, with a note when they differ
// (D-IDENTITY): a secret enrolled on a co-located instance stays within the
// agent's reach even after the instance is declared isolated.
func effectiveTier(configured string, enrollment owner.State, enrolled bool) (tier, note string) {
	if !enrolled || enrollment.Tier == configured {
		return configured, ""
	}
	return owner.TierCoLocated, tierNote
}
