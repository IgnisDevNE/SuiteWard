package governance

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ProcessConsent coordinates a domain command with its atomic stored outcome.
func ProcessConsent(ctx context.Context, store Store, request ConsentRequest) (ConsentResponse, error) {
	command := request.Command
	if store == nil || command.OperationID() == "" {
		return ConsentResponse{}, ErrInvalidRequest
	}
	snapshot, err := store.Load(ctx, ReadRequest{Reference: command.Reference(), OperationID: command.OperationID(), SourceCommandID: command.SourceCommandID()})
	if err != nil {
		return ConsentResponse{}, err
	}
	if !validConsentSnapshot(snapshot, command.Reference()) {
		return ConsentResponse{}, ErrInvalidSnapshot
	}
	next, result, err := snapshot.Consent.Apply(snapshot.Proposal, snapshot.Policy, command)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if result.Duplicate() {
		original := snapshot.Source.Consent
		if snapshot.Source.Kind != OperationConsent || original.Result.Duplicate() ||
			original.Result.Command() != result.Command() || original.Result.Outcome() != result.Outcome() || original.Result.Reason() != result.Reason() {
			return ConsentResponse{}, ErrInvalidSnapshot
		}
		if snapshot.Operation.Kind == 0 {
			if err := store.CommitConsent(ctx, snapshot.Fence, ConsentWrite{Command: command, Consent: next, Receipt: original, Alias: true}); err != nil {
				return ConsentResponse{}, err
			}
		} else if snapshot.Operation.Kind != OperationConsent || snapshot.Operation.Consent != original {
			return ConsentResponse{}, ErrInvalidSnapshot
		}
		return ConsentResponse{Receipt: original, Committed: true, Duplicate: true}, nil
	}
	receipt := ConsentReceipt{
		Result: result, EvaluatedReference: snapshot.Proposal.Current().Binding().Reference(),
		PolicyRevisionID: snapshot.Policy.RevisionID(), CurrentApprovalEligible: next.HasApproval(snapshot.Proposal, snapshot.Policy),
	}
	if err := store.CommitConsent(ctx, snapshot.Fence, ConsentWrite{Command: command, Consent: next, Receipt: receipt}); err != nil {
		return ConsentResponse{}, err
	}
	return ConsentResponse{Receipt: receipt, Committed: true}, nil
}

func validConsentSnapshot(snapshot Snapshot, reference contract.ProposalReference) bool {
	if snapshot.Canonical.IsZero() || snapshot.Proposal.IsZero() {
		return false
	}
	suite := snapshot.Canonical.Suite()
	current := snapshot.Proposal.Current().Binding().Reference()
	return suite.ProjectID() == reference.ProjectID && suite.ID() == reference.SuiteID &&
		snapshot.Fence == (AuthorityFence{ProjectID: suite.ProjectID(), SuiteID: suite.ID(), Revision: suite.Revision()}) &&
		current.ProjectID == reference.ProjectID && current.SuiteID == reference.SuiteID && current.ProposalID == reference.ProposalID &&
		snapshot.Policy.ProjectID() == reference.ProjectID
}
