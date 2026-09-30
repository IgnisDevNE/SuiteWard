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

var ErrInvalidIntegration = errors.New("invalid integration")

type IntegrationKind uint8
const (
	IntegrationMergedChange IntegrationKind = iota + 1
	IntegrationExistingBaseline
)

// Integration preserves trusted caller observations, not remote authentication.
type Integration struct{}
func NewIntegration(project ProjectID, target IntegrationTargetID, source SourceRevision, carrier ApprovalCarrierID, kind IntegrationKind) (Integration, error) {return Integration{},nil}
func (i Integration) ProjectID() ProjectID {return ""}
func (i Integration) Target() IntegrationTargetID {return ""}
func (i Integration) Source() SourceRevision {return ""}
func (i Integration) Carrier() ApprovalCarrierID {return ""}
func (i Integration) Kind() IntegrationKind {return 0}
func (i Integration) IsZero() bool {return true}

var ErrInvalidCanonicalSnapshot = errors.New("invalid canonical snapshot")

type ContractChange uint8

const (
	ContractUnchanged ContractChange = iota + 1
	ContractChanged
)

// CanonicalSnapshot couples a pointer with its complete protected contract.
type CanonicalSnapshot struct {
	suite     Suite
	version   SuiteVersion
	protected ProtectedContract
	record    PromotionRecord
}

func NewCanonicalSnapshot(suite Suite, version SuiteVersion, protected ProtectedContract, record PromotionRecord) (CanonicalSnapshot, error) {
	if suite.ID() == "" || suite.ProjectID() == "" {
		return CanonicalSnapshot{}, ErrInvalidCanonicalSnapshot
	}
	current, present := suite.CurrentVersionID()
	if !present {
		if version.ID() != "" || !protected.IsZero() || !record.IsZero() {
			return CanonicalSnapshot{}, ErrInvalidCanonicalSnapshot
		}
	} else if version.ID() != current || version.ProjectID() != suite.ProjectID() || version.SuiteID() != suite.ID() || protected.IsZero() || record.IsZero() ||
		version.Manifest().Digest() != protected.Manifest().Digest() || record.VersionID() != current || record.Binding().Reference().ProjectID != suite.ProjectID() || record.Binding().Reference().SuiteID != suite.ID() || !protected.matches(record.Binding()) {
		return CanonicalSnapshot{}, ErrInvalidCanonicalSnapshot
	}
	return CanonicalSnapshot{suite: suite, version: version, protected: protected, record: record}, nil
}
func (c CanonicalSnapshot) Suite() Suite                { return c.suite }
func (c CanonicalSnapshot) Version() SuiteVersion       { return c.version }
func (c CanonicalSnapshot) Contract() ProtectedContract { return c.protected }
func (c CanonicalSnapshot) Record() PromotionRecord     { return c.record }
func (c CanonicalSnapshot) IsZero() bool                { return c.suite.ID() == "" }

// ClassifyContractChange compares protected content without granting readiness.
func ClassifyContractChange(current CanonicalSnapshot, proposed ProtectedContract) (ContractChange, error) {
	if current.IsZero() {
		return 0, ErrInvalidCanonicalSnapshot
	}
	if proposed.IsZero() {
		return 0, ErrInvalidProtectedContract
	}
	if current.Contract().Equal(proposed) {
		return ContractUnchanged, nil
	}
	return ContractChanged, nil
}

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

func (p ProtectedContract) matches(binding ApprovalBinding) bool {
	return !p.IsZero() && !binding.IsZero() && p.manifest.Digest() == binding.ManifestDigest() && p.scope == binding.ScopeDigest() && maps.Equal(p.coveredInputs, binding.CoveredInputs())
}
