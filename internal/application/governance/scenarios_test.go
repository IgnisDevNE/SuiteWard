package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
