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
}

type BootstrapHeadResult struct {
	// Cancelled means the head is not the protected contract of the Suite's latest bootstrap proposal, as carried by Carrier:
	// there is no such proposal, it belongs to another PR or to the default branch, or the head's coverage differs from its
	// current revision or has no protected files. Nothing was written.
	Cancelled bool
	Reference *contract.ProposalReference // the current revision, nil when Cancelled
	Ready     bool                        // CheckPromotionReadiness answered ready for Head
	Reason    contract.PromotionReason
	Revision  int64 // the Suite revision this answer holds at
}

// AssessBootstrapHead assesses the head of the PR that carries the Suite's latest bootstrap proposal while there is no
// canonical, as AssessHead does for a change: it records the head's integrity assessment against the proposal's current
// revision and evaluates readiness for Head. It only assesses: it never proposes or revises, so a head whose coverage no
// longer matches the proposal is cancelled until the owner proposes again. The proposal is a baseline sourced from the PR head
// (OriginPull); it becomes canonical only by the merge of that PR.
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
	var proposed contract.ProtectedContract
	protected := len(inv.Manifest().Entries()) > 0
	if protected {
		if proposed, err = inv.Contract(); err != nil {
			return BootstrapHeadResult{}, fmt.Errorf("contract of the head: %w", err)
		}
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
		cancelled := BootstrapHeadResult{Cancelled: true, Revision: int64(state.Canonical.Suite().Revision())}
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil {
			return fmt.Errorf("find the latest bootstrap proposal: %w", err)
		}
		if !found || !protected {
			result = cancelled
			return nil
		}
		proposal, consent, err := tx.Proposal(ctx, id)
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		latest := proposal.Current()
		if latest.Carrier() != request.Carrier || latest.OriginKind() != contract.OriginPull {
			result = cancelled
			return nil
		}
		current := latest.Binding().Reference()
		binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: current, Manifest: proposed.Manifest().Digest(),
			Scope: proposed.ScopeDigest(), PolicyRevision: state.Policy.RevisionID(), CoveredInputs: proposed.CoveredInputs()})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		if !latest.Binding().SameCoverage(binding) {
			result = cancelled
			return nil
		}
		evidence, err := HeadEvidence(inv, latest.Binding(), request.Head)
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
