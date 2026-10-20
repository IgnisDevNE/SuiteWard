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
	err = uow.Do(ctx, request.Project, request.Suite, func(ctx context.Context, tx Tx) (err error) {
		result, err = assessBootstrapHead(ctx, tx, request, inv, proposed, protected)
		return err
	})
	if err != nil {
		return BootstrapHeadResult{}, err
	}
	return result, nil
}

// assessBootstrapHead is the unit of work of AssessBootstrapHead. A cancelled answer holds at the revision read before
// any write; an assessed one at the revision after its own write.
func assessBootstrapHead(ctx context.Context, tx Tx, request BootstrapHeadRequest, inv inventory.Inventory, proposed contract.ProtectedContract, protected bool) (BootstrapHeadResult, error) {
	state, err := tx.Suite(ctx)
	if err != nil {
		return BootstrapHeadResult{}, fmt.Errorf("load suite: %w", err)
	}
	if current, present := state.Canonical.Suite().CurrentVersionID(); present {
		return BootstrapHeadResult{}, fmt.Errorf("%w: the suite has the canonical %q", ErrCanonicalMoved, current)
	}
	cancelled := BootstrapHeadResult{Cancelled: true, Revision: int64(state.Canonical.Suite().Revision())}
	proposal, consent, found, err := bootstrapProposalOf(ctx, tx, state, request, proposed, protected)
	if err != nil {
		return BootstrapHeadResult{}, err
	}
	if !found {
		return cancelled, nil
	}
	reference, decision, revision, err := recordHeadAssessment(ctx, tx, headEvaluation{state: state, proposal: proposal, consent: consent, inv: inv, proposed: proposed, carrier: request.Carrier, head: request.Head})
	if err != nil {
		return BootstrapHeadResult{}, err
	}
	return BootstrapHeadResult{Reference: &reference, Ready: decision.Outcome() == contract.PromotionReady, Reason: decision.Reason(), Revision: revision}, nil
}

// bootstrapProposalOf returns the Suite's latest bootstrap proposal with its consent when the head may be assessed
// against it: the head has protected files, the proposal is a pull proposal carried by request.Carrier, and the head covers
// what its current revision covers. found is false otherwise.
func bootstrapProposalOf(ctx context.Context, tx Tx, state SuiteState, request BootstrapHeadRequest, proposed contract.ProtectedContract, protected bool) (contract.Proposal, contract.Consent, bool, error) {
	id, found, err := tx.LatestBootstrapProposal(ctx)
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, false, fmt.Errorf("find the latest bootstrap proposal: %w", err)
	}
	if !found || !protected {
		return contract.Proposal{}, contract.Consent{}, false, nil
	}
	proposal, consent, err := tx.Proposal(ctx, id)
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, false, fmt.Errorf("load proposal: %w", err)
	}
	latest := proposal.Current()
	if latest.Carrier() != request.Carrier || latest.OriginKind() != contract.OriginPull {
		return contract.Proposal{}, contract.Consent{}, false, nil
	}
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: latest.Binding().Reference(), Manifest: proposed.Manifest().Digest(),
		Scope: proposed.ScopeDigest(), PolicyRevision: state.Policy.RevisionID(), CoveredInputs: proposed.CoveredInputs()})
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, false, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if !latest.Binding().SameCoverage(binding) {
		return contract.Proposal{}, contract.Consent{}, false, nil
	}
	return proposal, consent, true, nil
}
