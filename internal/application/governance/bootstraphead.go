package governance

import (
	"context"
	"fmt"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type BootstrapHeadRequest struct {
	Project contract.ProjectID
	Suite   contract.SuiteID
	Carrier contract.ApprovalCarrierID // the bootstrap PR (the caller chooses it, runtime contract)
	Head    contract.SourceRevision
	Tree    inventory.Tree
}

type BootstrapHeadResult struct {
	Empty     bool                        // the head has no protected files: nothing was written
	Cancelled bool                        // the head is not the latest bootstrap proposal's coverage: nothing was written
	Reference *contract.ProposalReference // the current revision, nil when Empty
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason
	Revision  int64
}

// AssessBootstrapHead keeps the bootstrap proposal of a PR current while there is no canonical, as AssessHead does for a
// change: it proposes the protected contract of Head (R1, then R<n+1> when the coverage changes), records the head's
// integrity assessment and evaluates readiness for Head. The proposal is a baseline sourced from the PR head
// (OriginPull); it becomes canonical only by the merge of that PR. A head with no protected files writes nothing.
func AssessBootstrapHead(ctx context.Context, uow UnitOfWork, request BootstrapHeadRequest) (BootstrapHeadResult, error) {
	switch {
	case uow == nil:
		return BootstrapHeadResult{}, fmt.Errorf("%w: no unit of work", ErrInvalidRequest)
	case request.Tree == nil:
		return BootstrapHeadResult{}, fmt.Errorf("%w: no tree", ErrInvalidRequest)
	case blank(string(request.Head)) || blank(string(request.Carrier)):
		return BootstrapHeadResult{}, fmt.Errorf("%w: a head assessment names a head and a carrier", ErrInvalidRequest)
	}
	inv, err := inventory.Build(ctx, request.Tree)
	if err != nil {
		return BootstrapHeadResult{}, fmt.Errorf("inventory of the head: %w", err)
	}
	if len(inv.Manifest().Entries()) == 0 {
		return BootstrapHeadResult{Empty: true}, nil
	}
	proposed, err := inv.Contract()
	if err != nil {
		return BootstrapHeadResult{}, fmt.Errorf("contract of the head: %w", err)
	}
	var result BootstrapHeadResult
	err = uow.Do(ctx, request.Project, request.Suite, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		if current, present := state.Canonical.Suite().CurrentVersionID(); present {
			return fmt.Errorf("%w: the suite has the canonical %q", ErrCanonicalMoved, current)
		}
		id, found, err := tx.BaselineProposalFor(ctx, request.Carrier)
		if err != nil {
			return fmt.Errorf("find the bootstrap proposal of %q: %w", request.Carrier, err)
		}
		next := 1
		if found {
			existing, _, err := tx.Proposal(ctx, id)
			if err != nil {
				return fmt.Errorf("load proposal: %w", err)
			}
			next = len(existing.Revisions()) + 1
		} else if id, err = tx.NextProposalID(ctx); err != nil {
			return fmt.Errorf("allocate a proposal id: %w", err)
		}
		suite := state.Canonical.Suite()
		reference := contract.ProposalReference{ProjectID: suite.ProjectID(), SuiteID: suite.ID(), ProposalID: id, RevisionID: contract.ProposalRevisionID("R" + strconv.Itoa(next))}
		_, err = reviseBaselineWithin(ctx, tx, state, reference, request.Carrier, request.Head, contract.OriginPull, proposed)
		if err != nil {
			return err
		}
		proposal, consent, err := tx.Proposal(ctx, id)
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		current := proposal.Current().Binding().Reference()
		evidence, err := HeadEvidence(inv, proposal.Current().Binding(), request.Head)
		if err != nil {
			return err
		}
		if err := tx.RecordAssessment(ctx, AssessmentWrite{Reference: current, Source: request.Head, Evidence: evidence}); err != nil {
			return fmt.Errorf("record the assessment of the head: %w", err)
		}
		assessment, found, err := tx.Assessment(ctx, current, request.Head)
		if err != nil {
			return fmt.Errorf("load assessment: %w", err)
		}
		if !found {
			return fmt.Errorf("%w: the assessment of %v at %q is missing right after it was recorded", ErrInvalidState, current, request.Head)
		}
		decision, err := contract.CheckPromotionReadiness(contract.PromotionContext{
			Canonical: state.Canonical, Proposed: proposed, Proposal: proposal, Reference: current, Carrier: request.Carrier,
			Policy: state.Policy, Consent: consent, Assessment: assessment,
		}, request.Head)
		if err != nil {
			return fmt.Errorf("check readiness: %w", err)
		}
		written, err := tx.Suite(ctx) // the revision this answer holds at, after its own writes
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		result = BootstrapHeadResult{Reference: &current, Ready: decision.Outcome() == contract.PromotionReady,
			Reason: decision.Reason(), Revision: int64(written.Canonical.Suite().Revision())}
		return nil
	})
	if err != nil {
		return BootstrapHeadResult{}, err
	}
	return result, nil
}
