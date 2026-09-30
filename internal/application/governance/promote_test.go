package governance

import (
	"context"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func approvedPromotionFixture(t *testing.T) storeFixture {
	t.Helper()
	f := newStoreFixture(t)
	f.snapshot.Consent = f.consentWrite(t).Consent
	return f
}

func TestPromoteCommitsStoredAuthorityAndQueueTogether(t *testing.T) {
	f := approvedPromotionFixture(t)
	store := newReferenceStore(t, f.snapshot)
	result, err := Promote(context.Background(), store, f.request)
	if err != nil || !result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("promotion was not committed: committed=%v duplicate=%v outcome=%v err=%v", result.Committed, result.Duplicate, result.Decision.Outcome(), err)
	}
	state := store.inspect()
	effect, present := result.Decision.Effect()
	if !present || effect.Version().ID() != f.request.NewVersionID || state.canonical.Version().ID() != effect.Version().ID() || state.canonical.Suite().Revision() != f.snapshot.Fence.Revision+1 {
		t.Fatal("committed result and canonical version disagree")
	}
	if len(state.versions) != 1 || len(state.promotions) != 1 || len(state.operations) != 1 || len(state.audits) != 1 || len(state.publications) != 1 {
		t.Fatal("promotion did not persist all required immutable records and publication intent")
	}
	receipt := state.operations[f.request.OperationID]
	if receipt.Kind != OperationPromote || receipt.Promotion.Identity.Kind != OperationPromote || receipt.Promotion.Identity.Request.Reference != f.request.Reference || !receipt.Promotion.Identity.Binding.Equal(f.snapshot.Proposal.Current().Binding()) {
		t.Fatal("committed receipt lost exact operation identity and binding")
	}
	entries := state.scheduling.Entries()
	if len(entries) != 1 || entries[0].State() != contract.SchedulePromoted || state.scheduling.Generation() != f.snapshot.Scheduling.Generation()+1 {
		t.Fatal("canonical commit did not release the completed scheduling entry atomically")
	}
	if f.snapshot.Canonical.Version().ID() != "" || f.snapshot.Scheduling.Entries()[0].State() != contract.ScheduleActive {
		t.Fatal("application mutated its supplied immutable authority values")
	}
}
