package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// receiptView lets a test change what the unit of work answers for the receipt of an operation or of a source command,
// to reach the states ProcessConsent checks but a consistent store cannot produce. A nil function answers as stored.
type receiptView struct {
	governance.UnitOfWork
	operation, source func(stored governance.OperationReceipt, found bool) (governance.OperationReceipt, bool)
}

func (v receiptView) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return v.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error {
		return fn(ctx, receiptTx{Tx: tx, view: v})
	})
}

type receiptTx struct {
	governance.Tx
	view receiptView
}

func (t receiptTx) Receipt(ctx context.Context, id contract.OperationID) (governance.OperationReceipt, bool, error) {
	stored, found, err := t.Tx.Receipt(ctx, id)
	if err != nil || t.view.operation == nil {
		return stored, found, err
	}
	stored, found = t.view.operation(stored, found)
	return stored, found, nil
}

func (t receiptTx) ReceiptBySource(ctx context.Context, id contract.SourceCommandID) (governance.OperationReceipt, bool, error) {
	stored, found, err := t.Tx.ReceiptBySource(ctx, id)
	if err != nil || t.view.source == nil {
		return stored, found, err
	}
	stored, found = t.view.source(stored, found)
	return stored, found, nil
}

func hidden(governance.OperationReceipt, bool) (governance.OperationReceipt, bool) {
	return governance.OperationReceipt{}, false
}

// consentReceiptOf is the consent receipt a store would hold for command, as an approval.
func consentReceiptOf(t *testing.T, command contract.Command) governance.OperationReceipt {
	t.Helper()
	result := must(contract.ReconstituteCommandResult(command, contract.ConsentApproved, contract.ConsentReasonNone))
	reference := command.Reference()
	return governance.OperationReceipt{Kind: governance.OperationConsent, ProjectID: reference.ProjectID, SuiteID: reference.SuiteID,
		Consent: &governance.ConsentReceipt{Result: result, EvaluatedReference: reference, PolicyRevisionID: policyID}}
}

// TestProcessConsentDecidesSimultaneousFaultsInOrder pins what ProcessConsent answers when two things are wrong at
// once: a receipt for the operation id before the receipt for the source command; a source receipt conflict before the
// Suite and the proposal are read; the conflict the domain reports before the check that history and receipts agree.
func TestProcessConsentDecidesSimultaneousFaultsInOrder(t *testing.T) {
	w, c := consentWorld(t, "revision-1", "revision-2")
	first := w.command(c, "revision-2", "approve-1", "comment-1", contract.ApproveConsent, 1)
	second := w.command(c, "revision-2", "approve-2", "comment-2", contract.ApproveConsent, 2)
	original := w.consent(first)
	w.consent(second)
	ghost := newCandidate(t, "ghost", "", protectedOf(t, "v1"))

	for _, tt := range []struct {
		name    string
		view    receiptView
		command contract.Command
		wantErr error // nil: a duplicate of the original receipt
	}{
		{"a replay by operation id before the eligibility of the edited command",
			receiptView{}, w.command(c, "revision-1", "approve-1", "comment-1", contract.RevokeConsent, 99), nil},
		{"a source command of another proposal before the proposal is loaded",
			receiptView{}, w.command(ghost, "revision-1", "approve-new", "comment-1", contract.ApproveConsent, 1), governance.ErrOperationConflict},
		{"an operation id of another source command before the proposal is loaded",
			receiptView{}, w.command(ghost, "revision-1", "approve-1", "comment-new", contract.ApproveConsent, 1), governance.ErrOperationConflict},
		{"the receipt of the operation before the receipt of the source command",
			receiptView{source: func(_ governance.OperationReceipt, _ bool) (governance.OperationReceipt, bool) {
				return consentReceiptOf(t, w.command(c, "revision-2", "other", "comment-1", contract.ApproveConsent, 1)), true
			}}, first, nil},
		{"a conflict of the domain before the check of history and receipts",
			receiptView{operation: hidden}, w.command(c, "revision-2", "approve-1", "comment-2", contract.ApproveConsent, 2), governance.ErrOperationConflict},
		{"a replay the receipts do not know after the domain found it",
			receiptView{source: hidden}, w.command(c, "revision-2", "approve-alias", "comment-1", contract.ApproveConsent, 1), governance.ErrInvalidState},
		{"a source the receipts know after the domain found it new",
			receiptView{source: func(_ governance.OperationReceipt, _ bool) (governance.OperationReceipt, bool) {
				return consentReceiptOf(t, w.command(c, "revision-2", "approve-3", "comment-3", contract.ApproveConsent, 3)), true
			}}, w.command(c, "revision-2", "approve-3", "comment-3", contract.ApproveConsent, 3), governance.ErrInvalidState},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.revision()
			view := tt.view
			view.UnitOfWork = w.mem
			response, err := governance.ProcessConsent(context.Background(), view, governance.ConsentRequest{Command: tt.command})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil || !response.Duplicate || response.Receipt != original.Receipt {
				t.Fatalf("response = %+v, error = %v; want a duplicate of the original receipt", response, err)
			}
			w.requireWrites(before, 0)
		})
	}
}
