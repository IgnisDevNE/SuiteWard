package contract

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

type ProposalID string
type ProposalRevisionID string
type SourceRevision string
type ApprovalCarrierID string

// ProposalReference addresses one exact revision within its owning context.
type ProposalReference struct {
	ProjectID  ProjectID
	SuiteID    SuiteID
	ProposalID ProposalID
	RevisionID ProposalRevisionID
}

// BindingInput explicitly identifies all inputs covered by human consent.
type BindingInput struct {
	Reference         ProposalReference
	ExpectedCanonical SuiteVersionID
	Manifest          artifact.Digest
	Scope             artifact.Digest
	PolicyRevision    PolicyRevisionID
	CoveredInputs     map[string]string
}

var ErrInvalidBinding = errors.New("invalid approval binding")

// ApprovalBinding is an immutable exact covered-input value, not authorization.
type ApprovalBinding struct {
	input BindingInput
	valid bool
}

func NewApprovalBinding(input BindingInput) (ApprovalBinding, error) {
	return ApprovalBinding{}, nil
}

func (b ApprovalBinding) IsZero() bool                       { return true }
func (b ApprovalBinding) Reference() ProposalReference       { return ProposalReference{} }
func (b ApprovalBinding) ExpectedCanonical() SuiteVersionID  { return "" }
func (b ApprovalBinding) ManifestDigest() artifact.Digest    { return artifact.Digest{} }
func (b ApprovalBinding) ScopeDigest() artifact.Digest       { return artifact.Digest{} }
func (b ApprovalBinding) PolicyRevisionID() PolicyRevisionID { return "" }
func (b ApprovalBinding) CoveredInputs() map[string]string   { return nil }
func (b ApprovalBinding) Equal(other ApprovalBinding) bool   { return false }
