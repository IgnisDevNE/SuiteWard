package governance

import (
	"context"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestProcessConsentCommitsOriginalApproval(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	before := store.inspect()
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
	if err != nil || !response.Committed || response.Duplicate {
		t.Fatalf("original command = %+v, %v; want committed original outcome", response, err)
	}
	receipt := response.Receipt
	if receipt.Result.Command() != f.command || receipt.Result.Outcome() != contract.ConsentApproved || receipt.Result.Duplicate() ||
		receipt.EvaluatedReference != f.command.Reference() || receipt.PolicyRevisionID != f.snapshot.Policy.RevisionID() ||
		!receipt.CurrentApprovalEligible || receipt.PromotedVersionID != "" {
		t.Fatalf("receipt lost original outcome or evaluated authority: %+v", receipt)
	}
	after := store.inspect()
	currentAfter, hasAfter := after.canonical.Suite().CurrentVersionID()
	currentBefore, hasBefore := before.canonical.Suite().CurrentVersionID()
	if !after.consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
		t.Fatal("committed consent does not authorize the exact current proposal")
	}
	if after.canonical.Suite().Revision() != before.canonical.Suite().Revision()+1 ||
		currentAfter != currentBefore || hasAfter != hasBefore ||
		!reflect.DeepEqual(after.promotions, before.promotions) || !reflect.DeepEqual(after.scheduling, before.scheduling) {
		t.Fatal("consent must advance the whole authority fence without promoting or rescheduling")
	}
	want := OperationReceipt{Kind: OperationConsent, Consent: receipt}
	if !reflect.DeepEqual(after.operations[f.command.OperationID()], want) || !reflect.DeepEqual(after.sources[f.command.SourceCommandID()], want) ||
		len(after.audits) != 1 || !reflect.DeepEqual(after.audits[0], want) || len(after.acknowledgments) != 1 || after.acknowledgments[0] != receipt || len(after.publications) != 0 {
		t.Fatal("original outcome, audit and acknowledgment were not stored together")
	}
	if before.consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) || len(before.operations) != 0 {
		t.Fatal("processing mutated the earlier immutable snapshot")
	}
}
