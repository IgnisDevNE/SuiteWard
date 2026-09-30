package contract

import (
	"errors"
	"maps"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var ErrInvalidProtectedContract = errors.New("invalid protected contract")

// IntegrationTargetID identifies the configured integration destination.
type IntegrationTargetID string

// ProtectedContract describes exact protected content, separately from authority.
type ProtectedContract struct {
	manifest      artifact.Manifest
	scope         artifact.Digest
	coveredInputs map[string]string
}

func NewProtectedContract(manifest artifact.Manifest, scope artifact.Digest, coveredInputs map[string]string) (ProtectedContract, error) {
	if manifest.IsZero() || scope.IsZero() {
		return ProtectedContract{}, ErrInvalidProtectedContract
	}
	for key, value := range coveredInputs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return ProtectedContract{}, ErrInvalidProtectedContract
		}
	}
	return ProtectedContract{manifest: manifest, scope: scope, coveredInputs: maps.Clone(coveredInputs)}, nil
}

func (p ProtectedContract) Manifest() artifact.Manifest      { return p.manifest }
func (p ProtectedContract) ScopeDigest() artifact.Digest     { return p.scope }
func (p ProtectedContract) CoveredInputs() map[string]string { return maps.Clone(p.coveredInputs) }
func (p ProtectedContract) Equal(other ProtectedContract) bool {
	return !p.IsZero() && !other.IsZero() && p.manifest.Digest() == other.manifest.Digest() && p.scope == other.scope && maps.Equal(p.coveredInputs, other.coveredInputs)
}
func (p ProtectedContract) IsZero() bool { return p.manifest.IsZero() }
