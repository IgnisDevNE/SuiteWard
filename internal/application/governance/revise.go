package governance

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ReviseProposal appends a revision to a proposal, or creates the proposal
// with its first revision, inside one unit of work. A revision that covers
// what the current one covers changes nothing: repeated observation and
// implementation-only pushes create no revision.
func ReviseProposal(ctx context.Context, uow UnitOfWork, request ReviseRequest) (ReviseResult, error) {
	revision := request.Revision
	if uow == nil || revision.IsZero() {
		return ReviseResult{}, ErrInvalidRequest
	}
	binding := revision.Binding()
	reference := binding.Reference()
	var result ReviseResult
	err := uow.Do(ctx, reference.ProjectID, reference.SuiteID, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		suite := state.Canonical.Suite()
		current, _ := suite.CurrentVersionID() // empty before bootstrap
		if reference.ProjectID != suite.ProjectID() || reference.SuiteID != suite.ID() ||
			binding.PolicyRevisionID() != state.Policy.RevisionID() || binding.ExpectedCanonical() != current {
			return fmt.Errorf("%w: a revision must name the locked suite and be bound to its governing policy and current canonical", ErrInvalidRequest)
		}
		proposal, _, err := tx.Proposal(ctx, reference.ProposalID)
		switch {
		case errors.Is(err, ErrNotFound):
			if _, err := contract.NewProposal(revision); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
			}
		case err != nil:
			return fmt.Errorf("load proposal: %w", err)
		default:
			latest := proposal.Current()
			if latest.Carrier() == revision.Carrier() && latest.Binding().SameCoverage(binding) {
				result = ReviseResult{Current: latest.Binding().Reference()}
				return nil
			}
			if _, err := proposal.Revise(revision); err != nil {
				return fmt.Errorf("revise proposal: %w", err)
			}
		}
		if err := tx.AppendProposalRevision(ctx, ProposalWrite{Revision: revision}); err != nil {
			return fmt.Errorf("append proposal revision: %w", err)
		}
		result = ReviseResult{Current: reference, Committed: true}
		return nil
	})
	if err != nil {
		return ReviseResult{}, err
	}
	return result, nil
}
