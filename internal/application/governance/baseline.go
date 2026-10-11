package governance

import (
	"context"
	"errors"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ContentWriter stores artifact bytes under their digest. It is idempotent:
// storing the same bytes again succeeds. filesystem.Store implements it.
type ContentWriter interface {
	Put(ctx context.Context, digest artifact.Digest, content io.Reader) error
}

type BaselineRequest struct {
	Reference contract.ProposalReference // the proposal and its new revision id
	Carrier   contract.ApprovalCarrierID // the PR that hosts the approval
	Origin    contract.SourceRevision    // the pinned principal-branch commit
	Tree      inventory.Tree             // that commit's source tree
}

type BaselineResult struct {
	Revise   ReviseResult               // as returned by the shared revision logic
	Contract contract.ProtectedContract // inventory.Contract(), to pass to Bootstrap as PromoteRequest.Proposed
}

// ProposeBaseline is not implemented yet.
func ProposeBaseline(ctx context.Context, uow UnitOfWork, content ContentWriter, request BaselineRequest) (BaselineResult, error) {
	return BaselineResult{}, errors.New("not implemented")
}
