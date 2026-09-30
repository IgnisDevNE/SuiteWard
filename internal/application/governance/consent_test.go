package governance

import (
	"context"
	"errors"
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

// consentStoreProbe retains the real store and only controls observed boundaries.
type consentStoreProbe struct {
	Store
	changeSnapshot func(*Snapshot)
	query ReadRequest
	loads, commits int
	commitError error
}

func (s *consentStoreProbe) Load(ctx context.Context, query ReadRequest) (Snapshot, error) {
	s.loads++
	s.query = query
	snapshot, err := s.Store.Load(ctx, query)
	if err == nil && s.changeSnapshot != nil { s.changeSnapshot(&snapshot) }
	return snapshot, err
}

func (s *consentStoreProbe) CommitConsent(ctx context.Context, fence AuthorityFence, write ConsentWrite) error {
	s.commits++
	if s.commitError != nil { return s.commitError }
	return s.Store.CommitConsent(ctx, fence, write)
}

func TestProcessConsentRejectsInvalidContext(t *testing.T) {
	f := newStoreFixture(t)
	t.Run("zero command before load", func(t *testing.T) {
		store := &consentStoreProbe{Store: newReferenceStore(t, f.snapshot)}
		response, err := ProcessConsent(context.Background(), store, ConsentRequest{})
		if !errors.Is(err, ErrInvalidRequest) || response != (ConsentResponse{}) || store.loads != 0 || store.commits != 0 {
			t.Fatalf("invalid request = %+v, %v, loads=%d commits=%d", response, err, store.loads, store.commits)
		}
	})
	for name, mutate := range map[string]func(*Snapshot){
		"zero canonical": func(s *Snapshot) { s.Canonical = contract.CanonicalSnapshot{} },
		"foreign project fence": func(s *Snapshot) { s.Fence.ProjectID = "other" },
		"foreign suite fence": func(s *Snapshot) { s.Fence.SuiteID = "other" },
		"different revision fence": func(s *Snapshot) { s.Fence.Revision++ },
		"zero proposal": func(s *Snapshot) { s.Proposal = contract.Proposal{} },
		"zero policy": func(s *Snapshot) { s.Policy = contract.Policy{} },
		"zero consent": func(s *Snapshot) { s.Consent = contract.Consent{} },
	} {
		t.Run(name, func(t *testing.T) {
			actual := newReferenceStore(t, f.snapshot)
			before := actual.inspect()
			store := &consentStoreProbe{Store: actual, changeSnapshot: mutate}
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
				t.Fatalf("invalid snapshot = %+v, %v, commits=%d", response, err, store.commits)
			}
		})
	}
}

func TestProcessConsentFailureHasNoAcknowledgment(t *testing.T) {
	for _, stage := range []string{"load", "consent", "receipt", "audit", "acknowledgment"} {
		t.Run(stage, func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			before := store.inspect()
			store.failAt = stage
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if !errors.Is(err, errReferenceFailure) || response != (ConsentResponse{}) || !reflect.DeepEqual(before, store.inspect()) {
				t.Fatalf("failed %s leaked success or state: %+v, %v", stage, response, err)
			}
		})
	}
	f := newStoreFixture(t)
	actual := newReferenceStore(t, f.snapshot)
	store := &consentStoreProbe{Store: actual, commitError: ErrAuthorityConflict}
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
	if !errors.Is(err, ErrAuthorityConflict) || response != (ConsentResponse{}) || store.commits != 1 || len(actual.inspect().acknowledgments) != 0 {
		t.Fatalf("conflicted commit = %+v, %v", response, err)
	}
	wantQuery := ReadRequest{Reference: f.command.Reference(), OperationID: f.command.OperationID(), SourceCommandID: f.command.SourceCommandID()}
	if store.query != wantQuery { t.Fatalf("load query = %+v, want %+v", store.query, wantQuery) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = ProcessConsent(ctx, actual, ConsentRequest{Command: f.command})
	if !errors.Is(err, context.Canceled) || response != (ConsentResponse{}) { t.Fatalf("canceled = %+v, %v", response, err) }
}
