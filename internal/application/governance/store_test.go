package governance

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// referenceStore is a test model, not a production persistence option.
type referenceStore struct {
	mu     sync.Mutex
	state  referenceState
	failAt string
}

var errReferenceFailure = errors.New("injected reference store failure")

type assessmentKey struct {
	reference contract.ProposalReference
	source    contract.SourceRevision
}

type referenceState struct {
	canonical       contract.CanonicalSnapshot
	policy          contract.Policy
	scheduling      contract.Schedule
	target          contract.IntegrationTargetID
	proposals       map[contract.ProposalID]contract.Proposal
	consents        map[contract.ProposalID]contract.Consent
	assessments     map[assessmentKey]contract.IntegrityAssessment
	versions        map[contract.SuiteVersionID]contract.SuiteVersion
	promotions      map[contract.SuiteVersionID]contract.PromotionRecord
	operations      map[contract.OperationID]OperationReceipt
	sources         map[contract.SourceCommandID]OperationReceipt
	audits          []OperationReceipt
	publications    []contract.PublicationIntent
	acknowledgments []ConsentReceipt
}

func (s referenceState) clone() referenceState {
	s.proposals = maps.Clone(s.proposals)
	s.consents = maps.Clone(s.consents)
	s.assessments = maps.Clone(s.assessments)
	s.versions = maps.Clone(s.versions)
	s.promotions = maps.Clone(s.promotions)
	s.operations = maps.Clone(s.operations)
	s.sources = maps.Clone(s.sources)
	s.audits = slices.Clone(s.audits)
	s.publications = slices.Clone(s.publications)
	s.acknowledgments = slices.Clone(s.acknowledgments)
	return s
}

func newReferenceStore(t *testing.T, snapshots ...Snapshot) *referenceStore {
	t.Helper()
	if len(snapshots) == 0 {
		t.Fatal("reference store requires a seed")
	}
	first := snapshots[0]
	s := referenceState{
		canonical: first.Canonical, policy: first.Policy, scheduling: first.Scheduling, target: first.Target,
		proposals: map[contract.ProposalID]contract.Proposal{}, consents: map[contract.ProposalID]contract.Consent{},
		assessments: map[assessmentKey]contract.IntegrityAssessment{}, versions: map[contract.SuiteVersionID]contract.SuiteVersion{},
		promotions: map[contract.SuiteVersionID]contract.PromotionRecord{}, operations: map[contract.OperationID]OperationReceipt{},
		sources: map[contract.SourceCommandID]OperationReceipt{},
	}
	for _, seed := range snapshots {
		ref := seed.Proposal.Current().Binding().Reference()
		if ref.ProjectID != first.Canonical.Suite().ProjectID() || ref.SuiteID != first.Canonical.Suite().ID() {
			t.Fatal("foreign fixture scope")
		}
		s.proposals[ref.ProposalID] = seed.Proposal
		s.consents[ref.ProposalID] = seed.Consent
		if seed.Assessment.Assurance() != 0 {
			s.assessments[assessmentKey{seed.Assessment.Binding().Reference(), seed.Assessment.Source()}] = seed.Assessment
		}
		if !seed.Canonical.Record().IsZero() {
			s.versions[seed.Canonical.Version().ID()] = seed.Canonical.Version()
			s.promotions[seed.Canonical.Version().ID()] = seed.Canonical.Record()
		}
		if !seed.History.IsZero() {
			s.versions[seed.History.Version().ID()] = seed.History.Version()
			s.promotions[seed.History.Version().ID()] = seed.History.Record()
		}
	}
	return &referenceStore{state: s}
}

func (s *referenceStore) inspect() referenceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.clone()
}

func (s *referenceStore) Load(ctx context.Context, request ReadRequest) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if s.failAt == "load" {
		return Snapshot{}, errReferenceFailure
	}
	state := s.state
	suite := state.canonical.Suite()
	if request.Reference.ProjectID != suite.ProjectID() || request.Reference.SuiteID != suite.ID() {
		return Snapshot{}, ErrNotFound
	}
	proposal, found := state.proposals[request.Reference.ProposalID]
	if !found {
		return Snapshot{}, ErrNotFound
	}
	var history contract.HistoricalCanonical
	if version, found := state.versions[request.HistoricalVersionID]; found {
		var err error
		history, err = contract.NewHistoricalCanonical(version, state.promotions[version.ID()])
		if err != nil {
			return Snapshot{}, err
		}
	}
	var historicalPromotion contract.PromotionRecord
	for _, record := range state.promotions {
		if record.Binding().Reference() == request.Reference {
			historicalPromotion = record
			break
		}
	}
	return Snapshot{
		Fence:     AuthorityFence{suite.ProjectID(), suite.ID(), suite.Revision()},
		Canonical: state.canonical, Proposal: proposal, Policy: state.policy, Consent: state.consents[request.Reference.ProposalID],
		Scheduling: state.scheduling, Assessment: state.assessments[assessmentKey{request.Reference, request.AssessmentSource}],
		Target: state.target, History: history, HistoricalPromotion: historicalPromotion,
		Operation: state.operations[request.OperationID], Source: state.sources[request.SourceCommandID],
	}, nil
}

func (s *referenceStore) checkFence(ctx context.Context, fence AuthorityFence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	suite := s.state.canonical.Suite()
	if fence != (AuthorityFence{suite.ProjectID(), suite.ID(), suite.Revision()}) {
		return ErrAuthorityConflict
	}
	return nil
}

func (s *referenceStore) CommitPromotion(ctx context.Context, fence AuthorityFence, write PromotionWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFence(ctx, fence); err != nil {
		return err
	}
	effect, ok := write.Receipt.Decision.Effect()
	if !ok {
		return ErrInvalidRequest
	}
	next := s.state.clone()
	canonical, err := contract.NewCanonicalSnapshot(effect.Suite(), effect.Version(), write.Receipt.Identity.Request.Proposed, effect.Promotion())
	if err != nil {
		return err
	}
	receipt := OperationReceipt{Kind: write.Receipt.Identity.Kind, Promotion: write.Receipt}
	next.versions[effect.Version().ID()] = effect.Version()
	if s.failAt == "version" {
		return errReferenceFailure
	}
	next.canonical = canonical
	if s.failAt == "pointer" {
		return errReferenceFailure
	}
	next.promotions[effect.Version().ID()] = effect.Promotion()
	if s.failAt == "promotion" {
		return errReferenceFailure
	}
	next.operations[effect.Promotion().OperationID()] = receipt
	if s.failAt == "receipt" {
		return errReferenceFailure
	}
	next.audits = append(next.audits, receipt)
	if s.failAt == "audit" {
		return errReferenceFailure
	}
	next.scheduling = write.Scheduling
	if s.failAt == "schedule" {
		return errReferenceFailure
	}
	next.publications = append(next.publications, effect.Publication())
	if s.failAt == "publication" {
		return errReferenceFailure
	}
	s.state = next
	return nil
}

func (s *referenceStore) CommitConsent(ctx context.Context, fence AuthorityFence, write ConsentWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFence(ctx, fence); err != nil {
		return err
	}
	next := s.state.clone()
	current := next.canonical.Suite()
	currentID, _ := current.CurrentVersionID()
	suite, err := contract.NewSuite(current.ProjectID(), current.ID(), currentID, current.Revision()+1)
	if err != nil {
		return err
	}
	next.canonical, err = contract.NewCanonicalSnapshot(suite, next.canonical.Version(), next.canonical.Contract(), next.canonical.Record())
	if err != nil {
		return err
	}
	receipt := OperationReceipt{Kind: OperationConsent, Consent: write.Receipt}
	next.consents[write.Command.Reference().ProposalID] = write.Consent
	if s.failAt == "consent" {
		return errReferenceFailure
	}
	next.operations[write.Command.OperationID()] = receipt
	next.sources[write.Command.SourceCommandID()] = receipt
	if s.failAt == "receipt" {
		return errReferenceFailure
	}
	next.audits = append(next.audits, receipt)
	if s.failAt == "audit" {
		return errReferenceFailure
	}
	next.acknowledgments = append(next.acknowledgments, write.Receipt)
	if s.failAt == "acknowledgment" {
		return errReferenceFailure
	}
	s.state = next
	return nil
}

var _ Store = (*referenceStore)(nil)

type storeFixture struct {
	snapshot Snapshot
	proposed contract.ProtectedContract
	request  PromoteRequest
	owner    contract.Principal
	command  contract.Command
}

func mustValue[T any](t *testing.T, value T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newStoreFixture(t *testing.T) storeFixture {
	t.Helper()
	owner, err := contract.NewPrincipal("owner", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := contract.NewPolicy("project", "policy-1", owner)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash([]byte("test"))}})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := contract.NewProtectedContract(manifest, artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"})
	if err != nil {
		t.Fatal(err)
	}
	ref := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal-1", RevisionID: "revision-1"}
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: ref, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: policy.RevisionID(), CoveredInputs: protected.CoveredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := contract.NewProposalRevision(binding, "candidate-1", "carrier-1")
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := contract.NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	suite, err := contract.NewSuite("project", "suite", "", 7)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
	if err != nil {
		t.Fatal(err)
	}
	consent, err := contract.NewConsent("project", "suite", ref.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.NewCommand(contract.CommandInput{OperationID: "approve-1", SourceCommandID: "comment-1", Actor: owner, Reference: ref, Carrier: "carrier-1", Action: contract.ApproveConsent, Order: 1})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := contract.NewSchedule("project", "suite")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err = schedule.Admit(proposal, true)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := contract.NewIntegrityEvidence("verifier", "merged-1", binding, contract.IntegrityPassed)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := contract.AssessIntegrity("merged-1", binding, &evidence)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := contract.NewIntegration("project", "default", "merged-1", "carrier-1", contract.IntegrationMergedChange)
	if err != nil {
		t.Fatal(err)
	}
	return storeFixture{snapshot: Snapshot{Fence: AuthorityFence{"project", "suite", 7}, Canonical: canonical, Proposal: proposal, Policy: policy, Consent: consent, Scheduling: schedule, Assessment: assessment, Target: "default"}, proposed: protected, owner: owner, command: command, request: PromoteRequest{OperationID: "promote-1", Reference: ref, Carrier: "carrier-1", Proposed: protected, AssessmentSource: "merged-1", Integration: integration, NewVersionID: "version-1", RecordedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}}
}

func (f storeFixture) consentWrite(t *testing.T) ConsentWrite {
	t.Helper()
	next, result, err := f.snapshot.Consent.Apply(f.snapshot.Proposal, f.snapshot.Policy, f.command)
	if err != nil {
		t.Fatal(err)
	}
	return ConsentWrite{Command: f.command, Consent: next, Receipt: ConsentReceipt{Result: result, EvaluatedReference: f.snapshot.Proposal.Current().Binding().Reference(), PolicyRevisionID: f.snapshot.Policy.RevisionID(), CurrentApprovalEligible: next.HasApproval(f.snapshot.Proposal, f.snapshot.Policy)}}
}

func (f storeFixture) promotionWrite(t *testing.T) PromotionWrite {
	t.Helper()
	c := f.snapshot
	decision, err := contract.DecidePromotion(contract.PromotionInput{Context: contract.PromotionContext{Canonical: c.Canonical, Proposed: f.proposed, Proposal: c.Proposal, Reference: f.request.Reference, Carrier: f.request.Carrier, Policy: c.Policy, Consent: c.Consent, Assessment: c.Assessment, Scheduling: c.Scheduling, ExpectedStateRevision: c.Fence.Revision, ExpectedSchedulingGeneration: c.Scheduling.Generation()}, Integration: f.request.Integration, Target: c.Target, OperationID: f.request.OperationID, NewVersionID: f.request.NewVersionID, RecordedAt: f.request.RecordedAt})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("fixture not promotable: %v", decision.Reason())
	}
	next, err := c.Scheduling.Observe(c.Proposal, c.Scheduling.Generation(), contract.ObservePromoted)
	if err != nil {
		t.Fatal(err)
	}
	return PromotionWrite{Receipt: PromotionReceipt{Identity: PromotionIdentity{Kind: OperationPromote, Request: f.request, Binding: c.Proposal.Current().Binding()}, Decision: decision}, Scheduling: next}
}

func TestReferenceStoreAuthorityFence(t *testing.T) {
	f := newStoreFixture(t)
	s := newReferenceStore(t, f.snapshot)
	read := ReadRequest{Reference: f.request.Reference, OperationID: f.command.OperationID(), SourceCommandID: f.command.SourceCommandID(), AssessmentSource: f.request.AssessmentSource}
	loaded, err := s.Load(context.Background(), read)
	if err != nil || !reflect.DeepEqual(loaded, f.snapshot) {
		t.Fatalf("coherent stored authority missing: revision=%d err=%v", loaded.Fence.Revision, err)
	}
	write := f.consentWrite(t)
	if err := s.CommitConsent(context.Background(), loaded.Fence, write); err != nil {
		t.Fatal(err)
	}
	state := s.inspect()
	if state.canonical.Suite().Revision() != 8 || !state.consents[f.request.Reference.ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) || len(state.operations) != 1 || len(state.sources) != 1 || len(state.audits) != 1 || len(state.acknowledgments) != 1 {
		t.Fatalf("consent and whole-authority fence did not commit together: %+v", state)
	}
	if state.scheduling.Generation() != f.snapshot.Scheduling.Generation() || state.canonical.Version().ID() != "" {
		t.Fatal("consent changed scheduling or canonical pointer")
	}
	if err := s.CommitConsent(context.Background(), loaded.Fence, write); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatalf("stale fence accepted: %v", err)
	}
	if !reflect.DeepEqual(state, s.inspect()) {
		t.Fatal("stale write mutated authority")
	}
	loaded, err = s.Load(context.Background(), read)
	if err != nil || loaded.Fence.Revision != 8 || loaded.Operation.Kind != OperationConsent || loaded.Source.Kind != OperationConsent {
		t.Fatalf("committed receipt not visible with current fence: %+v %v", loaded, err)
	}
	unknown := read
	unknown.Reference.RevisionID = "unknown"
	if got, err := s.Load(context.Background(), unknown); err != nil || got.Proposal.IsZero() {
		t.Fatalf("unknown revision hid real aggregate: %v", err)
	}
}

func TestReferenceStorePromotionCommit(t *testing.T) {
	f := newStoreFixture(t)
	f.snapshot.Consent = f.consentWrite(t).Consent
	s := newReferenceStore(t, f.snapshot)
	write := f.promotionWrite(t)
	if err := s.CommitPromotion(context.Background(), f.snapshot.Fence, write); err != nil {
		t.Fatal(err)
	}
	state := s.inspect()
	if state.canonical.Version().ID() != f.request.NewVersionID || state.canonical.Suite().Revision() != 8 || len(state.versions) != 1 || len(state.promotions) != 1 || len(state.operations) != 1 || len(state.audits) != 1 || len(state.publications) != 1 || !reflect.DeepEqual(state.scheduling, write.Scheduling) {
		t.Fatalf("promotion effects were not committed together: current=%q revision=%d versions=%d receipts=%d publications=%d", state.canonical.Version().ID(), state.canonical.Suite().Revision(), len(state.versions), len(state.operations), len(state.publications))
	}
	if err := s.CommitPromotion(context.Background(), f.snapshot.Fence, write); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatalf("stale promotion accepted: %v", err)
	}
	if !reflect.DeepEqual(state, s.inspect()) {
		t.Fatal("stale promotion mutated state")
	}
	loaded, err := s.Load(context.Background(), ReadRequest{Reference: f.request.Reference, OperationID: f.request.OperationID, HistoricalVersionID: f.request.NewVersionID})
	if err != nil || loaded.Operation.Kind != OperationPromote || loaded.History.Version().ID() != f.request.NewVersionID || loaded.HistoricalPromotion.VersionID() != f.request.NewVersionID {
		t.Fatalf("promotion history or receipt missing: %+v %v", loaded, err)
	}
}

func TestReferenceStoreFaultAtomicity(t *testing.T) {
	for _, point := range []string{"version", "pointer", "promotion", "receipt", "audit", "schedule", "publication"} {
		t.Run("promotion/"+point, func(t *testing.T) {
			f := newStoreFixture(t)
			f.snapshot.Consent = f.consentWrite(t).Consent
			s := newReferenceStore(t, f.snapshot)
			before := s.inspect()
			s.failAt = point
			write := f.promotionWrite(t)
			if err := s.CommitPromotion(context.Background(), f.snapshot.Fence, write); !errors.Is(err, errReferenceFailure) {
				t.Fatalf("failure at %s not surfaced: %v", point, err)
			}
			if !reflect.DeepEqual(before, s.inspect()) {
				t.Fatalf("failure at %s leaked partial promotion effects", point)
			}
			s.failAt = ""
			if err := s.CommitPromotion(context.Background(), f.snapshot.Fence, write); err != nil {
				t.Fatal(err)
			}
			if state := s.inspect(); len(state.operations) != 1 || len(state.publications) != 1 {
				t.Fatal("failed commit consumed identity or duplicated publication")
			}
		})
	}
	for _, point := range []string{"consent", "receipt", "audit", "acknowledgment"} {
		t.Run("consent/"+point, func(t *testing.T) {
			f := newStoreFixture(t)
			s := newReferenceStore(t, f.snapshot)
			before := s.inspect()
			s.failAt = point
			write := f.consentWrite(t)
			if err := s.CommitConsent(context.Background(), f.snapshot.Fence, write); !errors.Is(err, errReferenceFailure) {
				t.Fatalf("failure at %s not surfaced: %v", point, err)
			}
			if !reflect.DeepEqual(before, s.inspect()) {
				t.Fatalf("failure at %s leaked consent, audit or acknowledgment", point)
			}
			s.failAt = ""
			if err := s.CommitConsent(context.Background(), f.snapshot.Fence, write); err != nil {
				t.Fatal(err)
			}
			if state := s.inspect(); len(state.operations) != 1 || len(state.acknowledgments) != 1 {
				t.Fatal("failed commit consumed identity or duplicated acknowledgment")
			}
		})
	}
	t.Run("load", func(t *testing.T) {
		f := newStoreFixture(t)
		s := newReferenceStore(t, f.snapshot)
		before := s.inspect()
		s.failAt = "load"
		if got, err := s.Load(context.Background(), ReadRequest{Reference: f.request.Reference}); !errors.Is(err, errReferenceFailure) || !reflect.DeepEqual(got, Snapshot{}) {
			t.Fatalf("load failure returned authority: %v", err)
		}
		if !reflect.DeepEqual(before, s.inspect()) {
			t.Fatal("failed load mutated authority")
		}
	})
}

// approvedStoreFixture seeds consent through the same atomic boundary used by
// application calls, so its receipt indexes are complete for replay scenarios.
func approvedStoreFixture(t *testing.T) (storeFixture, *referenceStore) {
	t.Helper()
	f := newStoreFixture(t)
	s := newReferenceStore(t, f.snapshot)
	if err := s.CommitConsent(context.Background(), f.snapshot.Fence, f.consentWrite(t)); err != nil {
		t.Fatal(err)
	}
	var err error
	f.snapshot, err = s.Load(context.Background(), ReadRequest{Reference: f.request.Reference, AssessmentSource: f.request.AssessmentSource})
	if err != nil {
		t.Fatal(err)
	}
	return f, s
}

func TestReferenceStoreCommitValidation(t *testing.T) {
	t.Run("consent overflow", func(t *testing.T) {
		f := newStoreFixture(t)
		suite, err := contract.NewSuite("project", "suite", "", ^contract.StateRevision(0))
		if err != nil {
			t.Fatal(err)
		}
		f.snapshot.Canonical, err = contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
		if err != nil {
			t.Fatal(err)
		}
		f.snapshot.Fence.Revision = suite.Revision()
		s := newReferenceStore(t, f.snapshot)
		before := s.inspect()
		if err := s.CommitConsent(context.Background(), f.snapshot.Fence, f.consentWrite(t)); !errors.Is(err, ErrAuthorityExhausted) {
			t.Fatalf("overflow accepted: %v", err)
		}
		if !reflect.DeepEqual(before, s.inspect()) {
			t.Fatal("overflow mutated authority")
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*storeFixture, *referenceStore, *PromotionWrite)
		want   error
	}{
		{"operation collision", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			s.state.operations[f.request.OperationID] = OperationReceipt{Kind: OperationConsent}
		}, ErrOperationConflict},
		{"version collision", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			s.state.versions[f.request.NewVersionID] = f.snapshot.Canonical.Version()
		}, ErrVersionConflict},
		{"wrong effect revision", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			f.snapshot.Fence.Revision--
			*w = f.promotionWrite(t)
		}, ErrAuthorityConflict},
		{"stale schedule", func(f *storeFixture, s *referenceStore, w *PromotionWrite) { w.Scheduling = f.snapshot.Scheduling }, ErrInvalidRequest},
		{"wrong operation identity", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			w.Receipt.Identity.Request.OperationID = "other-operation"
		}, ErrInvalidRequest},
		{"wrong version identity", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			w.Receipt.Identity.Request.NewVersionID = "other-version"
		}, ErrInvalidRequest},
		{"wrong binding identity", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			w.Receipt.Identity.Binding = contract.ApprovalBinding{}
		}, ErrInvalidRequest},
		{"wrong kind", func(f *storeFixture, s *referenceStore, w *PromotionWrite) {
			w.Receipt.Identity.Kind = OperationConsent
		}, ErrInvalidRequest},
	} {
		t.Run("promotion/"+tc.name, func(t *testing.T) {
			f, s := approvedStoreFixture(t)
			originalFence := f.snapshot.Fence
			write := f.promotionWrite(t)
			// A stale real effect is generated from a real prior snapshot, not a
			// mutable duplicate of the domain's sealed effect type.
			if tc.name == "wrong effect revision" {
				write = f.promotionWrite(t)
				current := s.state.canonical
				suite, err := contract.NewSuite("project", "suite", "", current.Suite().Revision()+1)
				if err != nil {
					t.Fatal(err)
				}
				s.state.canonical, err = contract.NewCanonicalSnapshot(suite, current.Version(), current.Contract(), current.Record())
				if err != nil {
					t.Fatal(err)
				}
				originalFence.Revision++
			} else {
				tc.mutate(&f, s, &write)
			}
			before := s.inspect()
			if err := s.CommitPromotion(context.Background(), originalFence, write); !errors.Is(err, tc.want) {
				t.Fatalf("invalid commit accepted: got %v want %v", err, tc.want)
			}
			if !reflect.DeepEqual(before, s.inspect()) {
				t.Fatal("invalid promotion mutated stored state")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*storeFixture, *referenceStore, *ConsentWrite)
		want   error
	}{
		{"operation collision", func(f *storeFixture, s *referenceStore, w *ConsentWrite) {
			s.state.operations[f.command.OperationID()] = OperationReceipt{Kind: OperationPromote}
		}, ErrOperationConflict},
		{"source collision", func(f *storeFixture, s *referenceStore, w *ConsentWrite) {
			s.state.sources[f.command.SourceCommandID()] = OperationReceipt{Kind: OperationConsent}
		}, ErrOperationConflict},
		{"wrong aggregate", func(f *storeFixture, s *referenceStore, w *ConsentWrite) { w.Consent = f.snapshot.Consent }, ErrInvalidRequest},
		{"wrong outcome", func(f *storeFixture, s *referenceStore, w *ConsentWrite) { w.Receipt.Result = contract.CommandResult{} }, ErrInvalidRequest},
		{"wrong policy acknowledgment", func(f *storeFixture, s *referenceStore, w *ConsentWrite) {
			w.Receipt.PolicyRevisionID = "another-policy"
		}, ErrInvalidRequest},
		{"false eligibility acknowledgment", func(f *storeFixture, s *referenceStore, w *ConsentWrite) { w.Receipt.CurrentApprovalEligible = false }, ErrInvalidRequest},
		{"unknown alias", func(f *storeFixture, s *referenceStore, w *ConsentWrite) { w.Alias = true }, ErrOperationConflict},
	} {
		t.Run("consent/"+tc.name, func(t *testing.T) {
			f := newStoreFixture(t)
			s := newReferenceStore(t, f.snapshot)
			write := f.consentWrite(t)
			tc.mutate(&f, s, &write)
			before := s.inspect()
			if err := s.CommitConsent(context.Background(), f.snapshot.Fence, write); !errors.Is(err, tc.want) {
				t.Fatalf("invalid commit accepted: got %v want %v", err, tc.want)
			}
			if !reflect.DeepEqual(before, s.inspect()) {
				t.Fatal("invalid consent mutated stored state")
			}
		})
	}
}

func TestReferenceStoreConsentAlias(t *testing.T) {
	f, s := approvedStoreFixture(t)
	original := s.inspect()
	alias, err := contract.NewCommand(contract.CommandInput{OperationID: "alias-1", SourceCommandID: f.command.SourceCommandID(), Actor: f.owner, Reference: f.request.Reference, Carrier: f.command.Carrier(), Action: contract.RevokeConsent, Order: 2})
	if err != nil {
		t.Fatal(err)
	}
	next, result, err := f.snapshot.Consent.Apply(f.snapshot.Proposal, f.snapshot.Policy, alias)
	if err != nil || !result.Duplicate() {
		t.Fatalf("fixture replay failed: %v", err)
	}
	write := ConsentWrite{Command: alias, Consent: next, Receipt: original.sources[alias.SourceCommandID()].Consent, Alias: true}
	s.failAt = "alias"
	if err := s.CommitConsent(context.Background(), f.snapshot.Fence, write); !errors.Is(err, errReferenceFailure) {
		t.Fatalf("alias failure not surfaced: %v", err)
	}
	if !reflect.DeepEqual(original, s.inspect()) {
		t.Fatal("failed alias reserved identity")
	}
	s.failAt = ""
	if err := s.CommitConsent(context.Background(), f.snapshot.Fence, write); err != nil {
		t.Fatal(err)
	}
	state := s.inspect()
	if len(state.operations) != 2 || len(state.sources) != 1 || len(state.audits) != 1 || len(state.acknowledgments) != 1 || state.canonical.Suite().Revision() != f.snapshot.Fence.Revision+1 {
		t.Fatal("alias lost reservation or duplicated terminal effects")
	}
	if !reflect.DeepEqual(state.consents[f.request.Reference.ProposalID], next) || !reflect.DeepEqual(state.operations[alias.OperationID()], original.sources[alias.SourceCommandID()]) {
		t.Fatal("alias lost domain state or changed original receipt")
	}
	if !state.consents[f.request.Reference.ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
		t.Fatal("edited replay revoked original approval")
	}
}
