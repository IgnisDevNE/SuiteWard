package contract

import (
	"errors"
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
	Canonical                    CanonicalSnapshot
	Proposed                     ProtectedContract
	Proposal                     Proposal
	Reference                    ProposalReference
	Carrier                      ApprovalCarrierID
	Policy                       Policy
	Consent                      Consent
	Assessment                   IntegrityAssessment
	Scheduling                   Schedule
	ExpectedStateRevision        StateRevision
	ExpectedSchedulingGeneration ScheduleGeneration
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
	PromotionReasonStateChanged
	PromotionReasonPolicyChanged
	PromotionReasonApprovalMissing
	PromotionReasonAssessmentMismatch
	PromotionReasonIntegrityNotPassed
	PromotionReasonSchedulingBlocked
	PromotionReasonIntegrationMissing
	PromotionReasonIntegrationMismatch
	PromotionReasonCanonicalPresent
	PromotionReasonEmptyInventory
	PromotionReasonCorrectionContextReused
)

type PromotionInput struct {
	Context PromotionContext
	Integration Integration
	Target IntegrationTargetID
	OperationID OperationID
	NewVersionID SuiteVersionID
	RecordedAt time.Time
	CorrectsVersionID SuiteVersionID
}

// PromotionEffect proposes one atomic local write and its publication intent.
type PromotionEffect struct{}

func (e PromotionEffect) IsZero() bool { return true }
func (e PromotionEffect) ExpectedCanonicalID() SuiteVersionID {return ""}
func (e PromotionEffect) ExpectedStateRevision() StateRevision {return 0}
func (e PromotionEffect) ExpectedSchedulingGeneration() ScheduleGeneration {return 0}
func (e PromotionEffect) Suite() Suite {return Suite{}}
func (e PromotionEffect) Version() SuiteVersion {return SuiteVersion{}}
func (e PromotionEffect) Promotion() PromotionRecord {return PromotionRecord{}}
func (e PromotionEffect) Audit() AuditEvent {return AuditEvent{}}
func (e PromotionEffect) Publication() PublicationIntent {return PublicationIntent{}}

func DecidePromotion(input PromotionInput) (PromotionDecision,error) {return blockedPromotion(PromotionReasonNone),nil}

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
	if context.Canonical.IsZero() || context.Proposed.IsZero() || context.Proposal.IsZero() || !validProposalReference(context.Reference) ||
		strings.TrimSpace(string(context.Carrier)) == "" || context.Policy.RevisionID() == "" || strings.TrimSpace(string(requiredSource)) == "" {
		return PromotionDecision{}, ErrInvalidPromotion
	}
	revision, err := context.Proposal.Resolve(context.Reference, context.Carrier)
	if errors.Is(err, ErrProposalContextMismatch) {
		return blockedPromotion(PromotionReasonContextMismatch), nil
	}
	if err != nil {
		return blockedPromotion(PromotionReasonProposalNotCurrent), nil
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
	if context.ExpectedStateRevision != context.Canonical.Suite().Revision() {
		return blockedPromotion(PromotionReasonStateChanged), nil
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
	if !context.Scheduling.CanPromote(context.Proposal, context.ExpectedSchedulingGeneration) {
		return blockedPromotion(PromotionReasonSchedulingBlocked), nil
	}
	return PromotionDecision{outcome: PromotionReady}, nil
}
