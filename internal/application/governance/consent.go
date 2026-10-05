package governance

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ProcessConsent applies one command inside a unit of work and records its
// outcome together with the receipt that makes later replays exact.
func ProcessConsent(ctx context.Context, uow UnitOfWork, request ConsentRequest) (ConsentResponse, error) {
	command := request.Command
	reference := command.Reference()
	if uow == nil || command.OperationID() == "" {
		return ConsentResponse{}, ErrInvalidRequest
	}
	var response ConsentResponse
	err := uow.Do(ctx, reference.ProjectID, reference.SuiteID, func(ctx context.Context, tx Tx) error {
		stored, found, err := tx.Receipt(ctx, command.OperationID())
		if err != nil {
			return fmt.Errorf("load operation receipt: %w", err)
		}
		if found {
			if !isCommandReceipt(stored, command) {
				return ErrOperationConflict
			}
			response = ConsentResponse{Receipt: *stored.Consent, Committed: true, Duplicate: true}
			return nil
		}
		original, sourceKnown, err := tx.ReceiptBySource(ctx, command.SourceCommandID())
		if err != nil {
			return fmt.Errorf("load source command receipt: %w", err)
		}
		if sourceKnown && !isCommandReceipt(original, command) {
			return ErrOperationConflict
		}
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		proposal, consent, err := tx.Proposal(ctx, reference.ProposalID)
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		next, result, err := consent.Apply(proposal, state.Policy, command)
		if err != nil {
			return fmt.Errorf("apply consent command: %w", err)
		}
		switch {
		case result.Reason() == contract.ConsentReasonCommandConflict:
			return ErrOperationConflict
		case result.Duplicate() != sourceKnown:
			return fmt.Errorf("%w: consent history and receipts disagree about source command %q", ErrInvalidState, command.SourceCommandID())
		case result.Duplicate():
			if err := tx.AppendConsent(ctx, ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: *original.Consent, Alias: true}); err != nil {
				return fmt.Errorf("record consent alias: %w", err)
			}
			response = ConsentResponse{Receipt: *original.Consent, Committed: true, Duplicate: true}
			return nil
		}
		var promoted contract.SuiteVersionID
		record, promotedFound, err := tx.PromotionFor(ctx, reference)
		if err != nil {
			return fmt.Errorf("load promotion: %w", err)
		}
		if promotedFound {
			promoted = record.VersionID()
		}
		receipt := ConsentReceipt{
			Result: result, EvaluatedReference: proposal.Current().Binding().Reference(), PolicyRevisionID: state.Policy.RevisionID(),
			CurrentApprovalEligible: next.HasApproval(proposal, state.Policy), PromotedVersionID: promoted,
		}
		if err := tx.AppendConsent(ctx, ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: receipt}); err != nil {
			return fmt.Errorf("record consent: %w", err)
		}
		response = ConsentResponse{Receipt: receipt, Committed: true}
		return nil
	})
	if err != nil {
		return ConsentResponse{}, err
	}
	return response, nil
}

// isCommandReceipt reports whether stored is a consent receipt for the same
// source command, actor and proposal as command.
func isCommandReceipt(stored OperationReceipt, command contract.Command) bool {
	if stored.Kind != OperationConsent || stored.Consent == nil {
		return false
	}
	original := stored.Consent.Result.Command()
	wanted := command.Reference()
	return stored.ProjectID == wanted.ProjectID && stored.SuiteID == wanted.SuiteID && original.Reference().ProposalID == wanted.ProposalID &&
		original.SourceCommandID() == command.SourceCommandID() && original.Actor() == command.Actor()
}
