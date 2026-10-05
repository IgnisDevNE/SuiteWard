// Package governance coordinates domain decisions with atomic authority storage.
package governance

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// UnitOfWork runs fn while holding the lock of one Suite. Writes made through
// the Tx commit only if fn returns nil; any error rolls back every write.
// Do returns ErrNotFound when the Suite does not exist.
type UnitOfWork interface {
	Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, Tx) error) error
}

// SuiteState is the Suite-level authority read under the lock.
type SuiteState struct {
	Canonical contract.CanonicalSnapshot // Suite().Revision() is the stored counter; no current version before bootstrap
	Policy    contract.Policy            // governing policy revision
	Target    contract.IntegrationTargetID
}

// Tx is valid only inside fn. Reads see the transaction's own writes.
type Tx interface {
	Suite(context.Context) (SuiteState, error)
	// Proposal returns ErrNotFound when the proposal is unknown in this Suite.
	Proposal(context.Context, contract.ProposalID) (contract.Proposal, contract.Consent, error)
	Assessment(context.Context, contract.ProposalReference, contract.SourceRevision) (contract.IntegrityAssessment, bool, error)
	Version(context.Context, contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error)
	PromotionFor(context.Context, contract.ProposalReference) (contract.PromotionRecord, bool, error)
	// Receipt lookups are global, not limited to the locked Suite.
	Receipt(context.Context, contract.OperationID) (OperationReceipt, bool, error)
	ReceiptBySource(context.Context, contract.SourceCommandID) (OperationReceipt, bool, error)

	AppendConsent(context.Context, ConsentWrite) error
	RecordPromotion(context.Context, PromotionWrite) error
	// M1.2 adds Enqueue (River) and Outbox writes here.
}

// Seeder writes trusted initial state. It is the only writer of policies,
// proposals, assessments and historical versions until the
// GitHub-facing phases add their own write paths.
type Seeder interface {
	Seed(context.Context, Seed) error
}

type Seed struct {
	Suite     SuiteState                     // Canonical may have no current version
	History   []contract.HistoricalCanonical // earlier versions, oldest first; includes the current one when present
	Proposals []SeedProposal
}

type SeedProposal struct {
	Proposal    contract.Proposal // all revisions, in order
	Assessments []contract.IntegrityAssessment
}

type OperationKind string

const (
	OperationPromote   OperationKind = "promote"
	OperationBootstrap OperationKind = "bootstrap"
	OperationCorrect   OperationKind = "correct"
	OperationConsent   OperationKind = "consent"
)

// OperationReceipt is a sum type: exactly one of Promotion or Consent is set,
// matching Kind.
type OperationReceipt struct {
	Kind      OperationKind
	ProjectID contract.ProjectID
	SuiteID   contract.SuiteID
	Promotion *PromotionReceipt
	Consent   *ConsentReceipt
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
	Record   contract.PromotionRecord
}

type ConsentReceipt struct {
	Result                  contract.CommandResult
	EvaluatedReference      contract.ProposalReference
	PolicyRevisionID        contract.PolicyRevisionID
	CurrentApprovalEligible bool
	PromotedVersionID       contract.SuiteVersionID
}

// ConsentWrite appends one processed command. With Alias, Result is the
// duplicate observed for a new operation id and Receipt is the original
// receipt: the store records only the new operation id for the original
// source command.
type ConsentWrite struct {
	OperationID contract.OperationID
	Result      contract.CommandResult
	Receipt     ConsentReceipt
	Alias       bool
}

// PromotionWrite records a proposed promotion: the new version (its manifest
// bytes are verified now), the promotion record, and the new current pointer.
type PromotionWrite struct {
	Receipt PromotionReceipt
	Version contract.SuiteVersion
}

type PromoteResult struct {
	Outcome   contract.PromotionOutcome
	Reason    contract.PromotionReason
	Record    contract.PromotionRecord // set when Outcome is PromotionProposed
	Committed bool
	Duplicate bool
}

type ConsentRequest struct{ Command contract.Command }

type ConsentResponse struct {
	Receipt   ConsentReceipt
	Committed bool
	Duplicate bool
}

var (
	ErrInvalidRequest    = errors.New("invalid governance request")
	ErrInvalidState      = errors.New("invalid stored governance state")
	ErrOperationConflict = errors.New("operation identity conflict")
	ErrVersionConflict   = errors.New("version identity conflict")
	ErrNotFound          = errors.New("governance aggregate not found")
)
