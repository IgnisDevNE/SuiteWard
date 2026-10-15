package contract

import (
	"errors"
	"slices"
	"strings"
)

var ErrInvalidProposal = errors.New("invalid proposal")

var (
	ErrInvalidReference        = errors.New("invalid proposal reference")
	ErrProposalContextMismatch = errors.New("proposal context mismatch")
	ErrUnknownRevision         = errors.New("unknown proposal revision")
	ErrSupersededRevision      = errors.New("superseded proposal revision")
	ErrRevisionExists          = errors.New("proposal revision already exists")
)

// OriginKind says where a proposal revision's source comes from.
type OriginKind string

const (
	OriginBranch OriginKind = "branch" // a pinned default-branch commit (existing-baseline bootstrap)
	OriginPull   OriginKind = "pull"   // a PR head: the first-test bootstrap and every change proposal
)

// ProposalRevision seals inventory provenance and its separate consent carrier.
type ProposalRevision struct {
	binding ApprovalBinding
	origin  SourceRevision
	kind    OriginKind
	carrier ApprovalCarrierID
}

func NewProposalRevision(binding ApprovalBinding, origin SourceRevision, kind OriginKind, carrier ApprovalCarrierID) (ProposalRevision, error) {
	if binding.IsZero() || strings.TrimSpace(string(origin)) == "" || strings.TrimSpace(string(carrier)) == "" {
		return ProposalRevision{}, ErrInvalidProposal
	}
	switch {
	case kind == OriginPull:
	case kind == OriginBranch && binding.ExpectedCanonical() == "": // only a baseline comes from a branch
	default:
		return ProposalRevision{}, ErrInvalidProposal
	}
	return ProposalRevision{binding: binding, origin: origin, kind: kind, carrier: carrier}, nil
}

func (r ProposalRevision) Binding() ApprovalBinding   { return r.binding }
func (r ProposalRevision) Origin() SourceRevision     { return r.origin }
func (r ProposalRevision) OriginKind() OriginKind     { return r.kind }
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

func (p Proposal) Lookup(reference ProposalReference, carrier ApprovalCarrierID) (ProposalRevision, error) {
	if p.IsZero() {
		return ProposalRevision{}, ErrInvalidProposal
	}
	if !validProposalReference(reference) || strings.TrimSpace(string(carrier)) == "" {
		return ProposalRevision{}, ErrInvalidReference
	}
	current := p.Current()
	if !sameProposalContext(reference, current.Binding().Reference()) || carrier != current.Carrier() {
		return ProposalRevision{}, ErrProposalContextMismatch
	}
	for _, revision := range p.revisions {
		if revision.Binding().Reference().RevisionID == reference.RevisionID {
			return revision, nil
		}
	}
	return ProposalRevision{}, ErrUnknownRevision
}

func (p Proposal) Resolve(reference ProposalReference, carrier ApprovalCarrierID) (ProposalRevision, error) {
	revision, err := p.Lookup(reference, carrier)
	if err != nil {
		return ProposalRevision{}, err
	}
	if reference.RevisionID != p.Current().Binding().Reference().RevisionID {
		return ProposalRevision{}, ErrSupersededRevision
	}
	return revision, nil
}

func sameProposalContext(left, right ProposalReference) bool {
	return left.ProjectID == right.ProjectID && left.SuiteID == right.SuiteID && left.ProposalID == right.ProposalID
}

func (p Proposal) Revise(next ProposalRevision) (Proposal, error) {
	if p.IsZero() || next.IsZero() {
		return Proposal{}, ErrInvalidProposal
	}
	current := p.Current()
	if !sameProposalContext(next.Binding().Reference(), current.Binding().Reference()) || next.Carrier() != current.Carrier() {
		return Proposal{}, ErrProposalContextMismatch
	}
	for _, revision := range p.revisions {
		if revision.Binding().Reference().RevisionID == next.Binding().Reference().RevisionID {
			return Proposal{}, ErrRevisionExists
		}
	}
	if next.OriginKind() != p.revisions[0].OriginKind() { // a proposal never changes kind
		return Proposal{}, ErrInvalidProposal
	}
	return Proposal{revisions: append(slices.Clone(p.revisions), next)}, nil
}

// Revisions returns an independent copy of every revision, oldest first.
func (p Proposal) Revisions() []ProposalRevision { return slices.Clone(p.revisions) }
