package governance

import (
	"bytes"
	"context"
	"fmt"
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
	Reference  contract.ProposalReference // the proposal and its new revision id
	Carrier    contract.ApprovalCarrierID // the PR that hosts the approval
	Origin     contract.SourceRevision    // the pinned principal-branch commit, or the PR head of a pull-sourced bootstrap
	OriginKind contract.OriginKind        // OriginBranch for the bootstrap job, OriginPull for AssessBootstrapHead
	Tree       inventory.Tree             // that commit's source tree
	Outbox     []OutboxMessage            // written with a new revision, never when the coverage is unchanged
}

type BaselineResult struct {
	Revise   ReviseResult               // as returned by the shared revision logic
	Contract contract.ProtectedContract // inventory.Contract(), to pass to Bootstrap as PromoteRequest.Proposed
}

// ProposeBaseline proposes the protected contract of a pinned principal-branch
// commit as the Suite's first canonical. Content is stored before the
// database is touched: an orphan blob after a later failure is harmless, a
// version row without its bytes is not. A baseline of coverage the proposal
// already has changes nothing, as with ReviseProposal.
func ProposeBaseline(ctx context.Context, uow UnitOfWork, content ContentWriter, request BaselineRequest) (BaselineResult, error) {
	switch {
	case uow == nil:
		return BaselineResult{}, fmt.Errorf("%w: no unit of work", ErrInvalidRequest)
	case content == nil:
		return BaselineResult{}, fmt.Errorf("%w: no content writer", ErrInvalidRequest)
	case request.Tree == nil:
		return BaselineResult{}, fmt.Errorf("%w: no tree", ErrInvalidRequest)
	}
	inv, err := inventory.Build(ctx, request.Tree)
	if err != nil {
		return BaselineResult{}, fmt.Errorf("inventory of the baseline: %w", err)
	}
	proposed, err := inv.Contract()
	if err != nil {
		return BaselineResult{}, fmt.Errorf("baseline contract: %w", err)
	}
	if err := storeContent(ctx, request.Tree, proposed.Manifest(), content); err != nil {
		return BaselineResult{}, err
	}
	var revised ReviseResult
	err = uow.Do(ctx, request.Reference.ProjectID, request.Reference.SuiteID, func(ctx context.Context, tx Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		if _, present := state.Canonical.Suite().CurrentVersionID(); present {
			return fmt.Errorf("%w: a baseline is only proposed before the first canonical", ErrInvalidRequest)
		}
		revised, err = reviseBaselineWithin(ctx, tx, state, request.Reference, request.Carrier, request.Origin, contract.OriginBranch, proposed)
		if err != nil || !revised.Committed {
			return err
		}
		return writeOutbox(ctx, tx, request.Outbox)
	})
	if err != nil {
		return BaselineResult{}, err
	}
	return BaselineResult{Revise: revised, Contract: proposed}, nil
}

// reviseBaselineWithin makes the proposal at reference (a baseline expects no canonical) cover proposed, as the revision
// reference names, from origin of the given kind. The caller has checked, under the Suite lock, that there is no canonical.
// Coverage the proposal already has writes nothing.
func reviseBaselineWithin(ctx context.Context, tx Tx, state SuiteState, reference contract.ProposalReference, carrier contract.ApprovalCarrierID,
	origin contract.SourceRevision, kind contract.OriginKind, proposed contract.ProtectedContract) (ReviseResult, error) {
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: proposed.Manifest().Digest(),
		Scope: proposed.ScopeDigest(), PolicyRevision: state.Policy.RevisionID(), CoveredInputs: proposed.CoveredInputs()})
	if err != nil {
		return ReviseResult{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	revision, err := contract.NewProposalRevision(binding, origin, kind, carrier)
	if err != nil {
		return ReviseResult{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return reviseWithin(ctx, tx, state, revision)
}

// storeContent reads every entry of manifest from tree, checks the bytes against the manifest and stores them.
// A tree that changed since its inventory was taken is invalid, and the mismatching entry is not stored.
func storeContent(ctx context.Context, tree inventory.Tree, manifest artifact.Manifest, content ContentWriter) error {
	for _, entry := range manifest.Entries() {
		data, err := tree.Read(ctx, entry.Path)
		if err != nil {
			return fmt.Errorf("read %q: %w", entry.Path, err)
		}
		if artifact.Hash(data) != entry.Content {
			return fmt.Errorf("%w: %q changed while the tree was read", inventory.ErrInvalidTree, entry.Path)
		}
		if err := content.Put(ctx, entry.Content, bytes.NewReader(data)); err != nil {
			return fmt.Errorf("store %q: %w", entry.Path, err)
		}
	}
	return nil
}
