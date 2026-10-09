package contract

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

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
type Integration struct {
	project ProjectID
	target  IntegrationTargetID
	source  SourceRevision
	carrier ApprovalCarrierID
	kind    IntegrationKind
}

func NewIntegration(project ProjectID, target IntegrationTargetID, source SourceRevision, carrier ApprovalCarrierID, kind IntegrationKind) (Integration, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(target)) == "" || strings.TrimSpace(string(source)) == "" {
		return Integration{}, ErrInvalidIntegration
	}
	switch kind {
	case IntegrationMergedChange:
		if strings.TrimSpace(string(carrier)) == "" {
			return Integration{}, ErrInvalidIntegration
		}
	case IntegrationExistingBaseline:
		if carrier != "" {
			return Integration{}, ErrInvalidIntegration
		}
	default:
		return Integration{}, ErrInvalidIntegration
	}
	return Integration{project: project, target: target, source: source, carrier: carrier, kind: kind}, nil
}
func (i Integration) ProjectID() ProjectID        { return i.project }
func (i Integration) Target() IntegrationTargetID { return i.target }
func (i Integration) Source() SourceRevision      { return i.source }
func (i Integration) Carrier() ApprovalCarrierID  { return i.carrier }
func (i Integration) Kind() IntegrationKind       { return i.kind }
func (i Integration) IsZero() bool                { return i.kind == 0 }

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

var ErrInvalidPromotion = errors.New("invalid promotion")

// PromotionContext is a caller-supplied snapshot of all governing authority.
type PromotionContext struct {
	Canonical  CanonicalSnapshot
	Proposed   ProtectedContract
	Proposal   Proposal
	Reference  ProposalReference
	Carrier    ApprovalCarrierID
	Policy     Policy
	Consent    Consent
	Assessment IntegrityAssessment
}

type PromotionOutcome uint8

const (
	PromotionBlocked PromotionOutcome = iota + 1
	PromotionReady
	PromotionNoChange
	PromotionProposed
)

type PromotionReason uint8

const (
	PromotionReasonNone PromotionReason = iota
	PromotionReasonContextMismatch
	PromotionReasonProposalNotCurrent
	PromotionReasonCanonicalChanged
	PromotionReasonPolicyChanged
	PromotionReasonApprovalMissing
	PromotionReasonAssessmentMismatch
	PromotionReasonIntegrityNotPassed
	PromotionReasonIntegrationMissing
	PromotionReasonIntegrationMismatch
	PromotionReasonCanonicalPresent
	PromotionReasonEmptyInventory
	PromotionReasonCorrectionContextReused
)

type PromotionInput struct {
	Context           PromotionContext
	Integration       Integration
	Target            IntegrationTargetID
	OperationID       OperationID
	NewVersionID      SuiteVersionID
	RecordedAt        time.Time
	CorrectsVersionID SuiteVersionID
}

// PromotionEffect proposes one atomic local write.
type PromotionEffect struct {
	expectedCanonical SuiteVersionID
	suite             Suite
	version           SuiteVersion
	promotion         PromotionRecord
}

func (e PromotionEffect) IsZero() bool                        { return e.promotion.IsZero() }
func (e PromotionEffect) ExpectedCanonicalID() SuiteVersionID { return e.expectedCanonical }
func (e PromotionEffect) Suite() Suite                        { return e.suite }
func (e PromotionEffect) Version() SuiteVersion               { return e.version }
func (e PromotionEffect) Promotion() PromotionRecord          { return e.promotion }

// DecidePromotion proposes effects only; committing them under the per-Suite lock is an
// application obligation. No-change means only that no new canonical is needed.
func DecidePromotion(input PromotionInput) (PromotionDecision, error) {
	change, err := ClassifyContractChange(input.Context.Canonical, input.Context.Proposed)
	if err != nil {
		return PromotionDecision{}, fmt.Errorf("%w: %w", ErrInvalidPromotion, err)
	}
	if change == ContractUnchanged {
		return PromotionDecision{outcome: PromotionNoChange}, nil
	}
	if input.Integration.IsZero() {
		return blockedPromotion(PromotionReasonIntegrationMissing), nil
	}
	if strings.TrimSpace(string(input.Target)) == "" {
		return PromotionDecision{}, ErrInvalidPromotion
	}
	readiness, err := CheckPromotionReadiness(input.Context, input.Integration.Source())
	if err != nil || readiness.Outcome() != PromotionReady {
		return readiness, err
	}
	current := input.Context.Canonical.Suite()
	if input.Integration.ProjectID() != current.ProjectID() || input.Integration.Target() != input.Target {
		return blockedPromotion(PromotionReasonIntegrationMismatch), nil
	}
	if input.Integration.Kind() == IntegrationMergedChange {
		if input.Integration.Carrier() != input.Context.Carrier {
			return blockedPromotion(PromotionReasonIntegrationMismatch), nil
		}
	} else if _, present := current.CurrentVersionID(); present || input.Integration.Source() != input.Context.Proposal.Current().Origin() {
		return blockedPromotion(PromotionReasonIntegrationMismatch), nil
	}
	record, err := NewPromotionRecord(PromotionRecordInput{OperationID: input.OperationID, VersionID: input.NewVersionID, Binding: input.Context.Proposal.Current().Binding(), Carrier: input.Context.Carrier, Source: input.Integration.Source(), Target: input.Target, RecordedAt: input.RecordedAt, CorrectsVersionID: input.CorrectsVersionID})
	if err != nil {
		return PromotionDecision{}, fmt.Errorf("%w: %w", ErrInvalidPromotion, err)
	}
	// Cannot fail here: the version id was validated by NewPromotionRecord and the project, suite and manifest come from the validated canonical Suite and Proposed contract.
	version, err := NewSuiteVersion(current.ProjectID(), current.ID(), input.NewVersionID, input.Context.Proposed.Manifest())
	if err != nil {
		return PromotionDecision{}, ErrInvalidPromotion
	}
	suite, err := NewSuite(current.ProjectID(), current.ID(), input.NewVersionID, current.Revision()+1)
	if err != nil {
		return PromotionDecision{}, ErrInvalidPromotion
	}
	effect := PromotionEffect{expectedCanonical: record.Binding().ExpectedCanonical(), suite: suite, version: version, promotion: record}
	return PromotionDecision{outcome: PromotionProposed, effect: effect}, nil
}

type PromotionDecision struct {
	outcome PromotionOutcome
	reason  PromotionReason
	effect  PromotionEffect
}

func (d PromotionDecision) Outcome() PromotionOutcome       { return d.outcome }
func (d PromotionDecision) Reason() PromotionReason         { return d.reason }
func (d PromotionDecision) Effect() (PromotionEffect, bool) { return d.effect, !d.effect.IsZero() }
func blockedPromotion(reason PromotionReason) PromotionDecision {
	return PromotionDecision{outcome: PromotionBlocked, reason: reason}
}
func CheckPromotionReadiness(context PromotionContext, requiredSource SourceRevision) (PromotionDecision, error) {
	if context.Canonical.IsZero() || context.Proposed.IsZero() || context.Policy.RevisionID() == "" || strings.TrimSpace(string(requiredSource)) == "" {
		return PromotionDecision{}, ErrInvalidPromotion
	}
	// Resolve itself rejects a zero proposal, an invalid reference and a blank carrier.
	revision, err := context.Proposal.Resolve(context.Reference, context.Carrier)
	switch {
	case err == nil:
	case errors.Is(err, ErrProposalContextMismatch):
		return blockedPromotion(PromotionReasonContextMismatch), nil
	case errors.Is(err, ErrUnknownRevision), errors.Is(err, ErrSupersededRevision):
		return blockedPromotion(PromotionReasonProposalNotCurrent), nil
	default:
		return PromotionDecision{}, fmt.Errorf("%w: %w", ErrInvalidPromotion, err)
	}
	binding := revision.Binding()
	if context.Reference.ProjectID != context.Canonical.Suite().ProjectID() || context.Reference.SuiteID != context.Canonical.Suite().ID() || !context.Proposed.matches(binding) {
		return blockedPromotion(PromotionReasonContextMismatch), nil
	}
	current, present := context.Canonical.Suite().CurrentVersionID()
	if !present && len(context.Proposed.Manifest().Entries()) == 0 {
		return blockedPromotion(PromotionReasonEmptyInventory), nil
	}
	if binding.ExpectedCanonical() != current {
		return blockedPromotion(PromotionReasonCanonicalChanged), nil
	}
	if context.Policy.ProjectID() != context.Canonical.Suite().ProjectID() {
		return blockedPromotion(PromotionReasonContextMismatch), nil
	}
	if context.Policy.RevisionID() != binding.PolicyRevisionID() {
		return blockedPromotion(PromotionReasonPolicyChanged), nil
	}
	if !context.Consent.HasApproval(context.Proposal, context.Policy) {
		return blockedPromotion(PromotionReasonApprovalMissing), nil
	}
	if context.Assessment.Assurance() != IntegrityOnly {
		return blockedPromotion(PromotionReasonIntegrityNotPassed), nil
	}
	if context.Assessment.Source() != requiredSource || !context.Assessment.Binding().Equal(binding) || context.Assessment.Reason() == IntegrityReasonMismatch {
		return blockedPromotion(PromotionReasonAssessmentMismatch), nil
	}
	if !context.Assessment.Passed() {
		return blockedPromotion(PromotionReasonIntegrityNotPassed), nil
	}
	return PromotionDecision{outcome: PromotionReady}, nil
}
