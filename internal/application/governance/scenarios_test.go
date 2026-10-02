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

func TestGovernanceApplicationCompetingPromotions(t *testing.T) {
	for _, kind := range []string{"promotion", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			if _, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command}); err != nil {
				t.Fatal(err)
			}
			other := f.request
			other.OperationID = "competing-operation"
			other.NewVersionID = "competing-version"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release := make(chan struct{})
			gates := []*commitBarrierStore{{Store: store, reached: make(chan struct{}, 1), release: release}, {Store: store, reached: make(chan struct{}, 1), release: release}}
			type outcome struct {
				result PromoteResult
				err    error
			}
			done := make(chan outcome, 2)
			for i, request := range []PromoteRequest{f.request, other} {
				go func() {
					var result PromoteResult
					var err error
					if kind == "bootstrap" {
						result, err = Bootstrap(ctx, gates[i], BootstrapRequest{Mode: contract.FirstTestBootstrap, Promotion: request})
					} else {
						result, err = Promote(ctx, gates[i], request)
					}
					done <- outcome{result, err}
				}()
			}
			waitForCommit(t, ctx, gates[0])
			waitForCommit(t, ctx, gates[1])
			close(release)
			success, conflict := 0, 0
			for range 2 {
				out := <-done
				if out.err == nil && out.result.Committed && !out.result.Duplicate {
					success++
				} else if errors.Is(out.err, ErrAuthorityConflict) && reflect.DeepEqual(out.result, PromoteResult{}) {
					conflict++
				} else {
					t.Fatalf("unexpected competing result: %+v", out)
				}
			}
			state := store.inspect()
			if success != 1 || conflict != 1 || len(state.versions) != 1 || len(state.promotions) != 1 || len(state.publications) != 1 || len(state.operations) != 2 || len(state.audits) != 2 {
				t.Fatal("competing use cases did not leave exactly one durable winner")
			}
		})
	}
}

func scenarioProposal(t *testing.T, f storeFixture, id contract.ProposalID, revisionID contract.ProposalRevisionID, carrier contract.ApprovalCarrierID) contract.Proposal {
	t.Helper()
	ref := f.request.Reference
	ref.ProposalID = id
	ref.RevisionID = revisionID
	current, _ := f.snapshot.Canonical.Suite().CurrentVersionID()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: ref, ExpectedCanonical: current, Manifest: f.proposed.Manifest().Digest(), Scope: f.proposed.ScopeDigest(), PolicyRevision: f.snapshot.Policy.RevisionID(), CoveredInputs: f.proposed.CoveredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := contract.NewProposalRevision(binding, "scenario-source", carrier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := contract.NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func TestGovernanceApplicationAuthorityChanges(t *testing.T) {
	for _, change := range []string{"policy", "proposal", "transfer", "waiting closure"} {
		t.Run(change, func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			if _, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command}); err != nil {
				t.Fatal(err)
			}
			waiting := scenarioProposal(t, f, "waiting", "waiting-1", "waiting-carrier")
			before := store.inspect()
			fence := AuthorityFence{"project", "suite", before.canonical.Suite().Revision()}
			if err := store.updateAuthority(context.Background(), fence, func(next *referenceState) error {
				var err error
				next.scheduling, err = next.scheduling.Admit(waiting, true)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gate := &commitBarrierStore{Store: store, reached: make(chan struct{}, 1), release: make(chan struct{})}
			type outcome struct {
				result PromoteResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() { result, err := Promote(ctx, gate, f.request); done <- outcome{result, err} }()
			waitForCommit(t, ctx, gate)
			before = store.inspect()
			fence.Revision = before.canonical.Suite().Revision()
			var revised contract.Proposal
			if change == "proposal" {
				revised = scenarioProposal(t, f, f.request.Reference.ProposalID, "new-revision", f.request.Carrier)
			}
			var priority contract.PriorityCommand
			if change == "transfer" {
				var err error
				priority, err = contract.NewPriorityCommand(contract.PriorityCommandInput{OperationID: "priority-op", SourceCommandID: "priority-source", Actor: f.owner, ProjectID: "project", SuiteID: "suite", ProposalID: "waiting", Carrier: "waiting-carrier", Order: 1})
				if err != nil {
					t.Fatal(err)
				}
			}
			err := store.updateAuthority(context.Background(), fence, func(next *referenceState) error {
				var err error
				switch change {
				case "policy":
					next.policy, err = contract.NewPolicy("project", "policy-new", f.owner)
				case "proposal":
					next.proposals[f.request.Reference.ProposalID], err = next.proposals[f.request.Reference.ProposalID].Revise(revised.Current())
				case "transfer":
					next.scheduling, _, err = next.scheduling.RequestPriority(next.policy, priority)
				case "waiting closure":
					next.scheduling, err = next.scheduling.Observe(waiting, next.scheduling.Generation(), contract.ObserveClosedUnmerged)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			after := store.inspect()
			close(gate.release)
			out := <-done
			if !errors.Is(out.err, ErrAuthorityConflict) || !reflect.DeepEqual(out.result, PromoteResult{}) || !reflect.DeepEqual(after, store.inspect()) {
				t.Fatalf("stale use case committed after %s changed: %+v", change, out)
			}
			if after.canonical.Version().ID() != before.canonical.Version().ID() || after.canonical.Suite().Revision() != before.canonical.Suite().Revision()+1 {
				t.Fatal("authority mutation failed whole-snapshot fence")
			}
			if change == "waiting closure" && after.scheduling.Generation() != before.scheduling.Generation() {
				t.Fatal("waiting-only mutation incorrectly changed active generation")
			}
		})
	}
}

func TestGovernanceApplicationMergedQueueHold(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	waiting := scenarioProposal(t, f, "waiting", "waiting-1", "waiting-carrier")
	if err := store.updateAuthority(context.Background(), f.snapshot.Fence, func(next *referenceState) error {
		var err error
		next.scheduling, err = next.scheduling.Admit(waiting, true)
		if err != nil {
			return err
		}
		next.scheduling, err = next.scheduling.Observe(f.snapshot.Proposal, next.scheduling.Generation(), contract.ObserveIntegrated)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := store.inspect()
	result, err := Promote(context.Background(), store, f.request)
	if err != nil || result.Committed || result.Duplicate || result.Decision.Reason() != contract.PromotionReasonApprovalMissing {
		t.Fatalf("merged unapproved proposal did not remain blocked: %+v %v", result, err)
	}
	after := store.inspect()
	active, present := after.scheduling.Active()
	if !present || active.ProposalID() != f.request.Reference.ProposalID || active.State() != contract.ScheduleIntegratedPending || !reflect.DeepEqual(before, after) {
		t.Fatal("merged-but-unpromotable proposal released queue or wrote effects")
	}
}

func TestGovernanceApplicationPublicationFailure(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	if _, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command}); err != nil {
		t.Fatal(err)
	}
	before := store.inspect()
	store.failAt = "publication"
	if result, err := Promote(context.Background(), store, f.request); !errors.Is(err, errReferenceFailure) || !reflect.DeepEqual(result, PromoteResult{}) || !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("failed local intent persistence returned committed success or partial effects")
	}
	store.failAt = ""
	first, err := Promote(context.Background(), store, f.request)
	if err != nil || !first.Committed {
		t.Fatal("retry could not commit complete local promotion", err)
	}
	committed := store.inspect()
	// This is an external delivery failure simulation, not an implemented worker
	// or a proof of exactly-once publication. Durable intent remains recoverable.
	deliver := func(contract.PublicationIntent) error { return errReferenceFailure }
	if err := deliver(committed.publications[0]); !errors.Is(err, errReferenceFailure) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(committed, store.inspect()) {
		t.Fatal("external delivery failure rewrote durable intent")
	}
	if replay, err := Promote(context.Background(), store, f.request); err != nil || !replay.Committed || !replay.Duplicate || len(store.inspect().publications) != 1 {
		t.Fatal("retry duplicated intent after simulated delivery failure", err)
	}
}

func TestGovernanceApplicationCorrectionReconcilesRevocation(t *testing.T) {
	store, request := correctionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gate := &commitBarrierStore{Store: store, reached: make(chan struct{}, 1), release: make(chan struct{})}
	type outcome struct {
		result PromoteResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := Correct(ctx, gate, request); done <- outcome{result, err} }()
	waitForCommit(t, ctx, gate)
	owner, err := contract.NewPrincipal(store.inspect().policy.OwnerID(), contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	commandInput := contract.CommandInput{OperationID: "revoke-correction", SourceCommandID: "source-revoke-correction", Actor: owner, Reference: request.Promotion.Reference, Carrier: request.Promotion.Carrier, Action: contract.RevokeConsent, Order: 2}
	revoke, err := contract.NewCommand(commandInput)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: revoke}); err != nil || !result.Committed {
		t.Fatal("correction revocation failed", err)
	}
	before := store.inspect()
	close(gate.release)
	stale := <-done
	if !errors.Is(stale.err, ErrAuthorityConflict) || !reflect.DeepEqual(stale.result, PromoteResult{}) || !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("stale correction committed despite revocation")
	}
	blocked, err := Correct(context.Background(), store, request)
	if err != nil || blocked.Committed || blocked.Decision.Reason() != contract.PromotionReasonApprovalMissing || !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("fresh correction did not evaluate revoked consent", err)
	}
	commandInput.OperationID = "reapprove-correction"
	commandInput.SourceCommandID = "source-reapprove-correction"
	commandInput.Action = contract.ApproveConsent
	commandInput.Order = 3
	approve, err := contract.NewCommand(commandInput)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: approve}); err != nil || !result.Committed {
		t.Fatal("fresh correction approval failed", err)
	}
	approved := store.inspect()
	// Byte identity does not let evidence for a previous integrated source pass.
	otherSource := request
	otherSource.Promotion.AssessmentSource = "unassessed-correction-source"
	otherSource.Promotion.Integration, err = contract.NewIntegration("project", approved.target, otherSource.Promotion.AssessmentSource, request.Promotion.Carrier, contract.IntegrationMergedChange)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := Correct(context.Background(), store, otherSource); err != nil || result.Committed || result.Decision.Outcome() != contract.PromotionBlocked || !reflect.DeepEqual(approved, store.inspect()) {
		t.Fatal("correction reused evidence from another source", err)
	}
	result, err := Correct(context.Background(), store, request)
	if err != nil || !result.Committed || result.Duplicate {
		t.Fatal("fresh authorized correction failed", err)
	}
	after := store.inspect()
	target := before.versions[request.TargetVersionID]
	if after.canonical.Version().ID() == target.ID() || after.canonical.Version().Manifest().Digest() != target.Manifest().Digest() || after.canonical.Record().CorrectsVersionID() != target.ID() {
		t.Fatal("correction did not create distinct version with historical bytes and attribution")
	}
	for id, version := range before.versions {
		if !reflect.DeepEqual(version, after.versions[id]) || !reflect.DeepEqual(before.promotions[id], after.promotions[id]) {
			t.Fatal("correction rewrote immutable history")
		}
	}
	if len(after.versions) != len(before.versions)+1 || len(after.publications) != len(before.publications)+1 {
		t.Fatal("failed/stale correction reserved versions or duplicated intents")
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
