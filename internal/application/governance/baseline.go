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
	Reference contract.ProposalReference // the proposal and its new revision id
	Carrier   contract.ApprovalCarrierID // the PR that hosts the approval
	Origin    contract.SourceRevision    // the pinned principal-branch commit
	Tree      inventory.Tree             // that commit's source tree
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
	if uow == nil || content == nil || request.Tree == nil {
		return BaselineResult{}, ErrInvalidRequest
	}
	inv, err := inventory.Build(ctx, request.Tree)
	if err != nil {
		return BaselineResult{}, fmt.Errorf("inventory of the baseline: %w", err)
	}
	proposed, err := inv.Contract()
	if err != nil {
		return BaselineResult{}, fmt.Errorf("baseline contract: %w", err)
	}
	for _, entry := range proposed.Manifest().Entries() {
		data, err := request.Tree.Read(ctx, entry.Path)
		if err != nil {
			return BaselineResult{}, fmt.Errorf("read %q: %w", entry.Path, err)
		}
		if artifact.Hash(data) != entry.Content {
			return BaselineResult{}, fmt.Errorf("%w: %q changed while the baseline was read", inventory.ErrInvalidTree, entry.Path)
		}
		if err := content.Put(ctx, entry.Content, bytes.NewReader(data)); err != nil {
			return BaselineResult{}, fmt.Errorf("store %q: %w", entry.Path, err)
		}
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
		binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: request.Reference, Manifest: proposed.Manifest().Digest(),
			Scope: proposed.ScopeDigest(), PolicyRevision: state.Policy.RevisionID(), CoveredInputs: proposed.CoveredInputs()})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		revision, err := contract.NewProposalRevision(binding, request.Origin, request.Carrier)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		revised, err = reviseWithin(ctx, tx, state, revision)
		return err
	})
	if err != nil {
		return BaselineResult{}, err
	}
	return BaselineResult{Revise: revised, Contract: proposed}, nil
}
