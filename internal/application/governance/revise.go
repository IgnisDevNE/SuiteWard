package governance

import (
	"context"
	"errors"
)

// ReviseProposal appends a revision to a proposal, creating the proposal with
// its first revision, inside one unit of work.
func ReviseProposal(ctx context.Context, uow UnitOfWork, request ReviseRequest) (ReviseResult, error) {
	return ReviseResult{}, errors.New("governance: ReviseProposal is not implemented")
}
