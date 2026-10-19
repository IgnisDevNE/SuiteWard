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
		return ConsentResponse{}, fmt.Errorf("%w: a consent command needs a unit of work and an operation id", ErrInvalidRequest)
	}
	var response ConsentResponse
	err := uow.Do(ctx, reference.ProjectID, reference.SuiteID, func(ctx context.Context, tx Tx) (err error) {
		response, err = processConsent(ctx, tx, request)
		return err
	})
	if err != nil {
		return ConsentResponse{}, err
	}
	return response, nil
}

// processConsent runs in the unit of work of the command's Suite. The order of its checks is the contract: the receipts
// of the operation and of the source command answer or conflict before anything else is read, the conflict the domain
// finds comes before the check that history and receipts agree, and the writes come last.
func processConsent(ctx context.Context, tx Tx, request ConsentRequest) (ConsentResponse, error) {
	command := request.Command
	reference := command.Reference()
	byOperation, bySource, err := consentReceipts(ctx, tx, command)
	if err != nil {
		return ConsentResponse{}, err
	}
	if byOperation != nil {
		return ConsentResponse{Receipt: *byOperation, Duplicate: true}, nil
	}
	state, err := tx.Suite(ctx)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("load suite: %w", err)
	}
	proposal, consent, err := tx.Proposal(ctx, reference.ProposalID)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("load proposal: %w", err)
	}
	next, result, err := consent.Apply(proposal, state.Policy, command)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("apply consent command: %w", err)
	}
	switch {
	case result.Reason() == contract.ConsentReasonCommandConflict:
		return ConsentResponse{}, fmt.Errorf("%w: the operation or the source command of %q belongs to another command", ErrOperationConflict, command.OperationID())
	case result.Duplicate() != (bySource != nil):
		return ConsentResponse{}, fmt.Errorf("%w: consent history and receipts disagree about source command %q", ErrInvalidState, command.SourceCommandID())
	case result.Duplicate():
		if err := tx.AppendConsent(ctx, ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: *bySource, Alias: true}); err != nil {
			return ConsentResponse{}, fmt.Errorf("record consent alias: %w", err)
		}
		return ConsentResponse{Receipt: *bySource, Duplicate: true}, nil
	}
	var promoted contract.SuiteVersionID
	record, promotedFound, err := tx.PromotionFor(ctx, reference)
	if err != nil {
		return ConsentResponse{}, fmt.Errorf("load promotion: %w", err)
	}
	if promotedFound {
		promoted = record.VersionID()
	}
	receipt := ConsentReceipt{
		Result: result, EvaluatedReference: proposal.Current().Binding().Reference(), PolicyRevisionID: state.Policy.RevisionID(),
		CurrentApprovalEligible: next.HasApproval(proposal, state.Policy), PromotedVersionID: promoted,
	}
	if err := tx.AppendConsent(ctx, ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: receipt}); err != nil {
		return ConsentResponse{}, fmt.Errorf("record consent: %w", err)
	}
	if err := writeOutbox(ctx, tx, request.Outbox); err != nil {
		return ConsentResponse{}, err
	}
	return ConsentResponse{Receipt: receipt}, nil
}

// consentReceipts reads the receipt of the command's operation id and, when there is none, the receipt of its source
// command. A receipt of the operation is the answer to a replay; a receipt of the source command is the original the
// command repeats under a new operation id. Either one that is not the receipt of this command conflicts.
func consentReceipts(ctx context.Context, tx Tx, command contract.Command) (byOperation, bySource *ConsentReceipt, err error) {
	stored, found, err := tx.Receipt(ctx, command.OperationID())
	if err != nil {
		return nil, nil, fmt.Errorf("load operation receipt: %w", err)
	}
	if found {
		byOperation, err = commandReceipt(stored, command)
		return byOperation, nil, err
	}
	original, found, err := tx.ReceiptBySource(ctx, command.SourceCommandID())
	if err != nil {
		return nil, nil, fmt.Errorf("load source command receipt: %w", err)
	}
	if !found {
		return nil, nil, nil
	}
	bySource, err = commandReceipt(original, command)
	return nil, bySource, err
}

// commandReceipt returns the consent receipt in stored when it is the receipt of the same source command, actor and
// proposal as command. Anything else, another kind of receipt included, is an operation conflict; a consent receipt
// without its body is damaged stored state.
func commandReceipt(stored OperationReceipt, command contract.Command) (*ConsentReceipt, error) {
	if stored.Kind != OperationConsent {
		return nil, fmt.Errorf("%w: the receipt is not a consent receipt", ErrOperationConflict)
	}
	if stored.Consent == nil {
		return nil, fmt.Errorf("%w: a consent receipt without its body", ErrInvalidState)
	}
	original := stored.Consent.Result.Command()
	wanted := command.Reference()
	if stored.ProjectID != wanted.ProjectID || stored.SuiteID != wanted.SuiteID || original.Reference().ProposalID != wanted.ProposalID ||
		original.SourceCommandID() != command.SourceCommandID() || original.Actor() != command.Actor() {
		return nil, fmt.Errorf("%w: the receipt belongs to another source command, actor or proposal", ErrOperationConflict)
	}
	return stored.Consent, nil
}
