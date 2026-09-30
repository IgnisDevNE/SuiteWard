package contract

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var ErrInvalidProtectedContract = errors.New("invalid protected contract")

// ProtectedContract describes exact protected content, separately from authority.
type ProtectedContract struct{}

func NewProtectedContract(manifest artifact.Manifest, scope artifact.Digest, coveredInputs map[string]string) (ProtectedContract, error) {
	return ProtectedContract{}, nil
}

func (p ProtectedContract) Manifest() artifact.Manifest { return artifact.Manifest{} }
func (p ProtectedContract) ScopeDigest() artifact.Digest { return artifact.Digest{} }
func (p ProtectedContract) CoveredInputs() map[string]string { return nil }
func (p ProtectedContract) Equal(other ProtectedContract) bool { return false }
func (p ProtectedContract) IsZero() bool { return true }
