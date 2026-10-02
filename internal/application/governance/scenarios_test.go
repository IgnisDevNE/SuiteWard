package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// These aggregate scenarios compose the separately TDD-proven domain and store
// behavior. Channel barriers choose interleavings; no sleep implies ordering.
func TestReferenceStoreCompetingPromotions(t *testing.T) {
	f, s := approvedStoreFixture(t)
	other := f
	other.request.OperationID = "promote-2"
	other.request.NewVersionID = "version-2"
	writes := []PromotionWrite{f.promotionWrite(t), other.promotionWrite(t)}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for _, write := range writes {
		go func() {
			ready <- struct{}{}
			<-release
			results <- s.CommitPromotion(context.Background(), f.snapshot.Fence, write)
		}()
	}
	<-ready
	<-ready
	close(release)
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrAuthorityConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	state := s.inspect()
	if success != 1 || conflict != 1 || len(state.versions) != 1 || len(state.promotions) != 1 || len(state.publications) != 1 || len(state.operations) != 2 || len(state.audits) != 2 {
		t.Fatalf("competing promotions did not produce exactly one complete winner: successes=%d conflicts=%d", success, conflict)
	}
	if state.canonical.Suite().Revision() != f.snapshot.Fence.Revision+1 {
		t.Fatal("losing promotion advanced authority")
	}
	for _, write := range writes {
		id := write.Receipt.Identity.Request.OperationID
		_, committed := state.operations[id]
		_, versionPresent := state.versions[write.Receipt.Identity.Request.NewVersionID]
		if committed != versionPresent {
			t.Fatal("winner receipt and immutable version disagree")
		}
	}
}

// commitBarrierStore leaves the real Load and commit implementation intact and
// pauses only the call boundary to select a race after application evaluation.
type commitBarrierStore struct {
	Store
	reached chan struct{}
	release chan struct{}
}

func (s *commitBarrierStore) wait(ctx context.Context) error {
	s.reached <- struct{}{}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *commitBarrierStore) CommitPromotion(ctx context.Context, fence AuthorityFence, write PromotionWrite) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.Store.CommitPromotion(ctx, fence, write)
}
func (s *commitBarrierStore) CommitConsent(ctx context.Context, fence AuthorityFence, write ConsentWrite) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.Store.CommitConsent(ctx, fence, write)
}

func waitForCommit(t *testing.T, ctx context.Context, store *commitBarrierStore) {
	t.Helper()
	select {
	case <-store.reached:
	case <-ctx.Done():
		t.Fatal("application never reached its commit boundary", ctx.Err())
	}
}

func TestGovernanceApplicationPromotionRevocationRace(t *testing.T) {
	for _, first := range []string{"promotion", "revocation"} {
		t.Run(first+" wins", func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			approved, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if err != nil || !approved.Committed || approved.Duplicate {
				t.Fatalf("approval did not commit: %+v %v", approved, err)
			}
			revoke, err := contract.NewCommand(contract.CommandInput{OperationID: "revoke-race", SourceCommandID: "source-revoke-race", Actor: f.owner, Reference: f.request.Reference, Carrier: f.request.Carrier, Action: contract.RevokeConsent, Order: 2})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			promotionGate := &commitBarrierStore{Store: store, reached: make(chan struct{}, 1), release: make(chan struct{})}
			revocationGate := &commitBarrierStore{Store: store, reached: make(chan struct{}, 1), release: make(chan struct{})}
			type promotionOutcome struct {
				result PromoteResult
				err    error
			}
			type consentOutcome struct {
				result ConsentResponse
				err    error
			}
			promotionDone, consentDone := make(chan promotionOutcome, 1), make(chan consentOutcome, 1)
			go func() {
				result, err := Promote(ctx, promotionGate, f.request)
				promotionDone <- promotionOutcome{result, err}
			}()
			go func() {
				result, err := ProcessConsent(ctx, revocationGate, ConsentRequest{Command: revoke})
				consentDone <- consentOutcome{result, err}
			}()
			waitForCommit(t, ctx, promotionGate)
			waitForCommit(t, ctx, revocationGate)
			var promoted promotionOutcome
			var revoked consentOutcome
			if first == "promotion" {
				close(promotionGate.release)
				promoted = <-promotionDone
				close(revocationGate.release)
				revoked = <-consentDone
			} else {
				close(revocationGate.release)
				revoked = <-consentDone
				close(promotionGate.release)
				promoted = <-promotionDone
			}
			state := store.inspect()
			if first == "promotion" {
				if promoted.err != nil || !promoted.result.Committed || promoted.result.Duplicate || !errors.Is(revoked.err, ErrAuthorityConflict) || !reflect.DeepEqual(revoked.result, ConsentResponse{}) {
					t.Fatalf("wrong ordered results: promote=%+v revoke=%+v", promoted, revoked)
				}
				if len(state.versions) != 1 || len(state.publications) != 1 || len(state.acknowledgments) != 1 {
					t.Fatal("losing revoke changed committed effects")
				}
			} else {
				if revoked.err != nil || !revoked.result.Committed || revoked.result.Duplicate || !errors.Is(promoted.err, ErrAuthorityConflict) || !reflect.DeepEqual(promoted.result, PromoteResult{}) {
					t.Fatalf("wrong ordered results: promote=%+v revoke=%+v", promoted, revoked)
				}
				if len(state.versions) != 0 || len(state.publications) != 0 || len(state.acknowledgments) != 2 || state.consents[f.request.Reference.ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
					t.Fatal("revocation did not prevent stale application promotion")
				}
			}
			if len(state.operations) != 2 || len(state.audits) != 2 || state.canonical.Suite().Revision() != f.snapshot.Fence.Revision+2 {
				t.Fatal("race committed more than one terminal contender")
			}
		})
	}
}

func TestGovernanceApplicationHistoricalPromotionReplay(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	if _, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command}); err != nil {
		t.Fatal(err)
	}
	first, err := Promote(context.Background(), store, f.request)
	if err != nil || !first.Committed {
		t.Fatalf("initial promotion failed: %v", err)
	}
	fence := AuthorityFence{"project", "suite", store.inspect().canonical.Suite().Revision()}
	if err := store.updateAuthority(context.Background(), fence, func(next *referenceState) error { delete(next.proposals, f.request.Reference.ProposalID); return nil }); err != nil {
		t.Fatal(err)
	}
	before := store.inspect()
	retry := f.request
	retry.RecordedAt = retry.RecordedAt.Add(time.Hour)
	replayed, err := Promote(context.Background(), store, retry)
	if err != nil || !replayed.Committed || !replayed.Duplicate || !reflect.DeepEqual(replayed.Decision, first.Decision) {
		t.Fatalf("historical replay required current authority or rewrote metadata: %+v %v", replayed, err)
	}
	conflict := retry
	conflict.Reference.SuiteID = "other-suite"
	if result, err := Promote(context.Background(), store, conflict); !errors.Is(err, ErrOperationConflict) || !reflect.DeepEqual(result, PromoteResult{}) {
		t.Fatalf("cross-scope identity adopted original receipt: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("replay or conflicting request changed durable history")
	}
}

func TestReferenceStorePromotionRevocationOrder(t *testing.T) {
	for _, first := range []string{"promotion", "revocation"} {
		t.Run(first+" wins", func(t *testing.T) {
			f, s := approvedStoreFixture(t)
			promotion := f.promotionWrite(t)
			revoke, err := contract.NewCommand(contract.CommandInput{OperationID: "revoke-1", SourceCommandID: "comment-revoke", Actor: f.owner, Reference: f.request.Reference, Carrier: f.request.Carrier, Action: contract.RevokeConsent, Order: 2})
			if err != nil {
				t.Fatal(err)
			}
			f.command = revoke
			revocation := f.consentWrite(t)
			promoteGate, revokeGate := make(chan struct{}), make(chan struct{})
			ready := make(chan struct{}, 2)
			promoted, revoked := make(chan error, 1), make(chan error, 1)
			go func() {
				ready <- struct{}{}
				<-promoteGate
				promoted <- s.CommitPromotion(context.Background(), f.snapshot.Fence, promotion)
			}()
			go func() {
				ready <- struct{}{}
				<-revokeGate
				revoked <- s.CommitConsent(context.Background(), f.snapshot.Fence, revocation)
			}()
			<-ready
			<-ready
			if first == "revocation" {
				close(revokeGate)
				if err := <-revoked; err != nil {
					t.Fatal(err)
				}
				before := s.inspect()
				close(promoteGate)
				if err := <-promoted; !errors.Is(err, ErrAuthorityConflict) {
					t.Fatalf("promotion ignored committed revocation: %v", err)
				}
				if !reflect.DeepEqual(before, s.inspect()) || len(before.publications) != 0 || before.consents[f.request.Reference.ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
					t.Fatal("revocation loser published or retained approval")
				}
			} else {
				close(promoteGate)
				if err := <-promoted; err != nil {
					t.Fatal(err)
				}
				before := s.inspect()
				close(revokeGate)
				if err := <-revoked; !errors.Is(err, ErrAuthorityConflict) {
					t.Fatalf("revocation ignored committed promotion fence: %v", err)
				}
				if !reflect.DeepEqual(before, s.inspect()) {
					t.Fatal("stale revocation changed promoted authority")
				}
				// Explicit reconciliation gets current authority and records an exact
				// historical acknowledgment; it cannot erase committed history.
				f.snapshot, err = s.Load(context.Background(), ReadRequest{Reference: f.request.Reference, OperationID: revoke.OperationID(), SourceCommandID: revoke.SourceCommandID()})
				if err != nil {
					t.Fatal(err)
				}
				revocation = f.consentWrite(t)
				revocation.Receipt.PromotedVersionID = f.snapshot.HistoricalPromotion.VersionID()
				if err := s.CommitConsent(context.Background(), f.snapshot.Fence, revocation); err != nil {
					t.Fatal(err)
				}
				after := s.inspect()
				if !reflect.DeepEqual(before.versions, after.versions) || !reflect.DeepEqual(before.promotions, after.promotions) || !reflect.DeepEqual(before.publications, after.publications) || before.canonical.Version().ID() != after.canonical.Version().ID() || !reflect.DeepEqual(before.scheduling, after.scheduling) {
					t.Fatal("post-promotion revoke rewrote immutable history or queue")
				}
				if after.consents[f.request.Reference.ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) || after.acknowledgments[len(after.acknowledgments)-1].PromotedVersionID != f.request.NewVersionID {
					t.Fatal("reconciled revoke lost historical promotion attribution")
				}
			}
		})
	}
}
