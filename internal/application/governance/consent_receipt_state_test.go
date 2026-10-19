package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// A receipt of kind consent always holds its ConsentReceipt. One that does not is damaged stored state, which is not a
// conflict between two commands, so ProcessConsent reports it as invalid state however it came to the receipt.
func TestProcessConsentReportsAConsentReceiptWithoutItsBodyAsInvalidState(t *testing.T) {
	w, c := consentWorld(t)
	original := w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1)
	w.consent(original)
	damaged := func(governance.OperationReceipt, bool) (governance.OperationReceipt, bool) {
		return governance.OperationReceipt{Kind: governance.OperationConsent, ProjectID: projectID, SuiteID: suiteID}, true
	}
	for name, view := range map[string]receiptView{
		"found by operation id": {operation: damaged},
		"found by source":       {source: damaged},
	} {
		t.Run(name, func(t *testing.T) {
			before := w.revision()
			view.UnitOfWork = w.mem
			retry := w.command(c, "revision-1", "approve-retry", "comment-retry", contract.ApproveConsent, 2)
			if name == "found by operation id" {
				retry = original
			}
			_, err := governance.ProcessConsent(context.Background(), view, governance.ConsentRequest{Command: retry})
			if !errors.Is(err, governance.ErrInvalidState) {
				t.Fatalf("error = %v, want invalid state", err)
			}
			w.requireWrites(before, 0)
		})
	}
}
