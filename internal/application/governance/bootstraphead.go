package governance

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type BootstrapHeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // the bootstrap PR (the caller chooses it, runtime contract)
	Head    contract.SourceRevision
	Tree    inventory.Tree
	// Summary builds the bootstrap-summary comment of a revision this call commits.
	Summary func(contract.ProposalReference, inventory.Inventory) (OutboxMessage, error)
}

type BootstrapHeadResult struct {
	Empty     bool                        // the head has no protected files: nothing was written
	Reference *contract.ProposalReference // the current revision, nil when Empty
	Committed bool                        // this call appended the revision
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason
	Revision  int64
}

// AssessBootstrapHead is not implemented yet.
func AssessBootstrapHead(ctx context.Context, uow UnitOfWork, writer ContentWriter, request BootstrapHeadRequest) (BootstrapHeadResult, error) {
	return BootstrapHeadResult{}, fmt.Errorf("%w: AssessBootstrapHead is not implemented", ErrInvalidState)
}
