package governance

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ErrNoCanonical reports that the Suite has no canonical yet: no change proposal exists before one does (D-CHECKS).
var ErrNoCanonical = errors.New("the suite has no canonical yet")

// ContentReader reads artifact bytes by digest. filesystem.Store implements it.
type ContentReader interface {
	Read(ctx context.Context, digest artifact.Digest) ([]byte, error)
}

type HeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // github-pr:<repository id>/<pull>
	Head    contract.SourceRevision    // the PR head SHA
	Tree    inventory.Tree             // that commit's source tree (AssessHead only)
}

type HeadResult struct {
	Change    inventory.Change            // Unchanged, ProtectedChange or ScopeReduction
	Reference *contract.ProposalReference // the PR's current revision; nil when Unchanged
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason    // when not Ready
	Canonical contract.SuiteVersionID
	Revision  int64 // the Suite revision the result was computed at
}

var errNotImplemented = errors.New("not implemented")

func AssessHead(ctx context.Context, uow UnitOfWork, reader ContentReader, writer ContentWriter, request HeadRequest) (HeadResult, error) {
	return HeadResult{}, errNotImplemented
}

func HeadEvidence(inv inventory.Inventory, binding contract.ApprovalBinding, source contract.SourceRevision) (contract.IntegrityEvidence, error) {
	return contract.IntegrityEvidence{}, errNotImplemented
}
