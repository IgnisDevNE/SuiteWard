package governance

import "context"

// ProcessConsent coordinates a domain command with its atomic stored outcome.
func ProcessConsent(ctx context.Context, store Store, request ConsentRequest) (ConsentResponse, error) {
	command := request.Command
	snapshot, err := store.Load(ctx, ReadRequest{Reference: command.Reference(), OperationID: command.OperationID(), SourceCommandID: command.SourceCommandID()})
	if err != nil {
		return ConsentResponse{}, err
	}
	next, result, err := snapshot.Consent.Apply(snapshot.Proposal, snapshot.Policy, command)
	if err != nil {
		return ConsentResponse{}, err
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
