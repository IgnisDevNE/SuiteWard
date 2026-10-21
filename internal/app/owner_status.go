package app

import (
	"context"
	"slices"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// tierNote is what /status and MCP status say when the configured tier and the
// tier recorded at enrollment differ.
const tierNote = "The configured tier and the tier recorded at enrollment differ, so the effective tier is co-located until setup is run again."

// shortInstance is the first 8 hex characters of the instance id: non-secret, stable per instance, and what
// setup puts in the authenticator entry and /status shows, so the owner can match one to the other.
// The store guarantees 32 hex characters (a CHECK constraint), so the slice cannot be out of range.
func shortInstance(id string) string { return id[:8] }

// ownerReader is the part of the owner store /status and MCP status read.
type ownerReader interface {
	Current(ctx context.Context) (owner.State, bool, error)
	Protection(ctx context.Context) ([]owner.ProtectionResult, error)
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

// unconfiguredRepositories lists the enrolled repositories that GITHUB_REPOSITORIES does not name, in enrollment
// order: the GitHub jobs skip them. Without GitHub settings the integration is off and none is listed.
func unconfiguredRepositories(configured []string, enrollment owner.State) []string {
	if len(configured) == 0 {
		return nil
	}
	var names []string
	for _, project := range enrollment.Projects {
		if !slices.Contains(configured, project.Repo) {
			names = append(names, project.Repo)
		}
	}
	return names
}
