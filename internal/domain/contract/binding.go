package contract

import (
	"errors"
	"maps"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ProposalID identifies a contract-change proposal independently of its provider.
type ProposalID string

// ProposalRevisionID identifies one immutable revision within a proposal.
type ProposalRevisionID string

// SourceRevision identifies exact source independently of its consent carrier.
type SourceRevision string

// ApprovalCarrierID identifies the conversation or change hosting consent.
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
	if !validProposalReference(input.Reference) || input.Manifest.IsZero() || input.Scope.IsZero() || strings.TrimSpace(string(input.PolicyRevision)) == "" {
		return ApprovalBinding{}, ErrInvalidBinding
	}
	if input.ExpectedCanonical != "" && strings.TrimSpace(string(input.ExpectedCanonical)) == "" {
		return ApprovalBinding{}, ErrInvalidBinding
	}
	for key, value := range input.CoveredInputs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return ApprovalBinding{}, ErrInvalidBinding
		}
	}
	input.CoveredInputs = maps.Clone(input.CoveredInputs)
	return ApprovalBinding{input: input, valid: true}, nil
}

func validProposalReference(reference ProposalReference) bool {
	return strings.TrimSpace(string(reference.ProjectID)) != "" &&
		strings.TrimSpace(string(reference.SuiteID)) != "" &&
		strings.TrimSpace(string(reference.ProposalID)) != "" &&
		strings.TrimSpace(string(reference.RevisionID)) != ""
}

func (b ApprovalBinding) IsZero() bool                       { return !b.valid }
func (b ApprovalBinding) Reference() ProposalReference       { return b.input.Reference }
func (b ApprovalBinding) ExpectedCanonical() SuiteVersionID  { return b.input.ExpectedCanonical }
func (b ApprovalBinding) ManifestDigest() artifact.Digest    { return b.input.Manifest }
func (b ApprovalBinding) ScopeDigest() artifact.Digest       { return b.input.Scope }
func (b ApprovalBinding) PolicyRevisionID() PolicyRevisionID { return b.input.PolicyRevision }
func (b ApprovalBinding) CoveredInputs() map[string]string   { return maps.Clone(b.input.CoveredInputs) }

// Equal compares usable bindings exactly; absence cannot satisfy a binding.
func (b ApprovalBinding) Equal(other ApprovalBinding) bool {
	return !b.IsZero() && !other.IsZero() &&
		b.input.Reference == other.input.Reference &&
		b.input.ExpectedCanonical == other.input.ExpectedCanonical &&
		b.input.Manifest == other.input.Manifest &&
		b.input.Scope == other.input.Scope &&
		b.input.PolicyRevision == other.input.PolicyRevision &&
		maps.Equal(b.input.CoveredInputs, other.input.CoveredInputs)
}

// SameCoverage compares what consent covers: manifest, scope, covered inputs,
// expected canonical and policy revision. The reference is ignored, so two
// revisions of one proposal can cover the same thing; absence cannot satisfy it.
func (b ApprovalBinding) SameCoverage(other ApprovalBinding) bool {
	return !b.IsZero() && !other.IsZero() &&
		b.input.ExpectedCanonical == other.input.ExpectedCanonical &&
		b.input.Manifest == other.input.Manifest &&
		b.input.Scope == other.input.Scope &&
		b.input.PolicyRevision == other.input.PolicyRevision &&
		maps.Equal(b.input.CoveredInputs, other.input.CoveredInputs)
}
