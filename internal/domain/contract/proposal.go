package contract

import (
	"errors"
	"strings"
)

var ErrInvalidProposal = errors.New("invalid proposal")

// ProposalRevision seals inventory provenance and its separate consent carrier.
type ProposalRevision struct {
	binding ApprovalBinding
	origin  SourceRevision
	carrier ApprovalCarrierID
}

func NewProposalRevision(binding ApprovalBinding, origin SourceRevision, carrier ApprovalCarrierID) (ProposalRevision, error) {
	if binding.IsZero() || strings.TrimSpace(string(origin)) == "" || strings.TrimSpace(string(carrier)) == "" {
		return ProposalRevision{}, ErrInvalidProposal
	}
	return ProposalRevision{binding: binding, origin: origin, carrier: carrier}, nil
}

func (r ProposalRevision) Binding() ApprovalBinding   { return r.binding }
func (r ProposalRevision) Origin() SourceRevision     { return r.origin }
func (r ProposalRevision) Carrier() ApprovalCarrierID { return r.carrier }
func (r ProposalRevision) IsZero() bool               { return r.binding.IsZero() }

// Proposal holds immutable exact revision history and its current revision.
type Proposal struct{ revisions []ProposalRevision }

func NewProposal(initial ProposalRevision) (Proposal, error) {
	if initial.IsZero() {
		return Proposal{}, ErrInvalidProposal
	}
	return Proposal{revisions: []ProposalRevision{initial}}, nil
}

func (p Proposal) Current() ProposalRevision {
	if p.IsZero() {
		return ProposalRevision{}
	}
	return p.revisions[len(p.revisions)-1]
}

func (p Proposal) IsZero() bool { return len(p.revisions) == 0 }
