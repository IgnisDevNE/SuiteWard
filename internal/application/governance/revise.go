package governance

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// reviseWithin applies revision to a proposal of the locked Suite, or creates the proposal with it, and writes nothing
// when the coverage is unchanged: repeated observation and implementation-only pushes create no revision. AssessHead
// and ProposeBaseline share it, and both bind the revision to the Suite state they just read under the lock, which is
// why it checks neither the Suite, the policy nor the canonical against that state.
func reviseWithin(ctx context.Context, tx Tx, revision contract.ProposalRevision) (ReviseResult, error) {
	binding := revision.Binding()
	reference := binding.Reference()
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
