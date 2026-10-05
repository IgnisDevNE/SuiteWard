// Package governance coordinates domain decisions with atomic authority storage.
package governance

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// AuthorityFence is the whole-Suite counter, exactly Canonical.Suite().Revision().
// All authority writes, including receipt aliases, advance this counter.
type AuthorityFence struct {
	ProjectID contract.ProjectID
	SuiteID   contract.SuiteID
	Revision  contract.StateRevision
}

type OperationKind uint8

const (
	OperationPromote OperationKind = iota + 1
	OperationBootstrap
	OperationCorrect
	OperationConsent
)

type ReadRequest struct {
	Reference           contract.ProposalReference
	OperationID         contract.OperationID
	SourceCommandID     contract.SourceCommandID
	AssessmentSource    contract.SourceRevision
	HistoricalVersionID contract.SuiteVersionID
}

// Snapshot is a coherent stored view, not a caller's authority assertion.
type Snapshot struct {
	Fence               AuthorityFence
	Canonical           contract.CanonicalSnapshot
	Proposal            contract.Proposal
	Policy              contract.Policy
	Consent             contract.Consent
	Scheduling          contract.Schedule
	Assessment          contract.IntegrityAssessment
	Target              contract.IntegrationTargetID
	History             contract.HistoricalCanonical
	HistoricalPromotion contract.PromotionRecord
	Operation           OperationReceipt
	Source              OperationReceipt
}

type PromoteRequest struct {
	OperationID      contract.OperationID
	Reference        contract.ProposalReference
	Carrier          contract.ApprovalCarrierID
	Proposed         contract.ProtectedContract
	AssessmentSource contract.SourceRevision
	Integration      contract.Integration
	NewVersionID     contract.SuiteVersionID
	RecordedAt       time.Time
}

type BootstrapRequest struct {
	Promotion PromoteRequest
}

type CorrectionRequest struct {
	Promotion       PromoteRequest
	TargetVersionID contract.SuiteVersionID
}

type PromotionIdentity struct {
	Kind              OperationKind
	Request           PromoteRequest
	CorrectsVersionID contract.SuiteVersionID
	Binding           contract.ApprovalBinding
}

type PromotionReceipt struct {
	Identity PromotionIdentity
	Decision contract.PromotionDecision
}

type ConsentReceipt struct {
	Result                  contract.CommandResult
	EvaluatedReference      contract.ProposalReference
	PolicyRevisionID        contract.PolicyRevisionID
	CurrentApprovalEligible bool
	PromotedVersionID       contract.SuiteVersionID
}

type OperationReceipt struct {
	Kind      OperationKind
	Promotion PromotionReceipt
	Consent   ConsentReceipt
}

type PromotionWrite struct {
	Receipt    PromotionReceipt
	Scheduling contract.Schedule
}

type ConsentWrite struct {
	Command contract.Command
	Consent contract.Consent
	Receipt ConsentReceipt
	Alias   bool
}

// Committed describes durability of the returned outcome, including replays.
type PromoteResult struct {
	Decision  contract.PromotionDecision
	Committed bool
	Duplicate bool
}

type ConsentRequest struct{ Command contract.Command }

type ConsentResponse struct {
	Receipt   ConsentReceipt
	Committed bool
	Duplicate bool
}

// Store loads coherent authority and atomically commits against its exact fence.
// Load does not retain a lock. Errors must leave every related effect unchanged.
// This port does not authenticate remote facts or promise durable publication.
type Store interface {
	Load(context.Context, ReadRequest) (Snapshot, error)
	CommitPromotion(context.Context, AuthorityFence, PromotionWrite) error
	CommitConsent(context.Context, AuthorityFence, ConsentWrite) error
}

var (
	ErrInvalidRequest    = errors.New("invalid governance request")
	ErrInvalidSnapshot   = errors.New("invalid governance snapshot")
	ErrAuthorityConflict = errors.New("authority fence conflict")
	ErrOperationConflict = errors.New("operation identity conflict")
	ErrVersionConflict   = errors.New("version identity conflict")
	ErrNotFound          = errors.New("governance aggregate not found")
)
