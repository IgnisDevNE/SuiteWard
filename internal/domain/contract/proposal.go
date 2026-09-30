package contract

import "errors"

var ErrInvalidProposal = errors.New("invalid proposal")

// ProposalRevision seals inventory provenance and its separate consent carrier.
type ProposalRevision struct {
	binding ApprovalBinding
	origin  SourceRevision
	carrier ApprovalCarrierID
}

func NewProposalRevision(binding ApprovalBinding, origin SourceRevision, carrier ApprovalCarrierID) (ProposalRevision, error) {
	return ProposalRevision{}, nil
}

func (r ProposalRevision) Binding() ApprovalBinding   { return ApprovalBinding{} }
func (r ProposalRevision) Origin() SourceRevision     { return "" }
func (r ProposalRevision) Carrier() ApprovalCarrierID { return "" }
func (r ProposalRevision) IsZero() bool               { return true }

// Proposal holds immutable exact revision history and its current revision.
type Proposal struct{ revisions []ProposalRevision }

func NewProposal(initial ProposalRevision) (Proposal, error) { return Proposal{}, nil }
func (p Proposal) Current() ProposalRevision                 { return ProposalRevision{} }
func (p Proposal) IsZero() bool                              { return true }
