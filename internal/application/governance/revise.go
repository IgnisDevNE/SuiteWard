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
	reference := revision.Binding().Reference()
	var result ReviseResult
	err := uow.Do(ctx, reference.ProjectID, reference.SuiteID, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		result, err = reviseWithin(ctx, tx, state, revision)
		return err
	})
	if err != nil {
		return ReviseResult{}, err
	}
	return result, nil
}

// reviseWithin applies revision to the locked Suite whose state was just
// read: it validates the revision against the Suite, creates or extends the
// proposal, and writes nothing when the coverage is unchanged. ReviseProposal
// and ProposeBaseline share it.
func reviseWithin(ctx context.Context, tx Tx, state SuiteState, revision contract.ProposalRevision) (ReviseResult, error) {
	binding := revision.Binding()
	reference := binding.Reference()
	suite := state.Canonical.Suite()
	current, _ := suite.CurrentVersionID() // empty before bootstrap
	if reference.ProjectID != suite.ProjectID() || reference.SuiteID != suite.ID() ||
		binding.PolicyRevisionID() != state.Policy.RevisionID() || binding.ExpectedCanonical() != current {
		return ReviseResult{}, fmt.Errorf("%w: a revision must name the locked suite and be bound to its governing policy and current canonical", ErrInvalidRequest)
	}
	proposal, _, err := tx.Proposal(ctx, reference.ProposalID)
	switch {
	case errors.Is(err, ErrNotFound):
		if _, err := contract.NewProposal(revision); err != nil {
			return ReviseResult{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
	case err != nil:
		return ReviseResult{}, fmt.Errorf("load proposal: %w", err)
	default:
		latest := proposal.Current()
		if latest.Carrier() == revision.Carrier() && latest.Binding().SameCoverage(binding) {
			return ReviseResult{Current: latest.Binding().Reference()}, nil
		}
		if _, err := proposal.Revise(revision); err != nil {
			return ReviseResult{}, fmt.Errorf("revise proposal: %w", err)
		}
	}
	if err := tx.AppendProposalRevision(ctx, ProposalWrite{Revision: revision}); err != nil {
		return ReviseResult{}, fmt.Errorf("append proposal revision: %w", err)
	}
	return ReviseResult{Current: reference, Committed: true}, nil
}
