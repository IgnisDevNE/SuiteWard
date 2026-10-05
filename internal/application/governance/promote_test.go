package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func approvedPromotionFixture(t *testing.T) storeFixture {
	t.Helper()
	f := newStoreFixture(t)
	f.snapshot.Consent = f.consentWrite(t).Consent
	return f
}

func TestPromoteReplaysOriginalReceiptBeforeCurrentAuthority(t *testing.T) {
	f, store := approvedStoreFixture(t)
	original, err := Promote(context.Background(), store, f.request)
	if err != nil || !original.Committed {
		t.Fatalf("setup: %+v %v", original, err)
	}
	state := store.inspect()
	err = store.updateAuthority(context.Background(), AuthorityFence{"project", "suite", state.canonical.Suite().Revision()}, func(s *referenceState) error {
		policy, err := contract.NewPolicy("project", "policy-2", f.owner)
		if err != nil {
			return err
		}
		s.policy = policy
		s.consents[f.request.Reference.ProposalID] = contract.Consent{}
		s.target = "another-target"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := store.inspect()
	retry := f.request
	retry.RecordedAt = retry.RecordedAt.Add(time.Hour)
	result, err := Promote(context.Background(), store, retry)
	if err != nil || !result.Committed || !result.Duplicate || !reflect.DeepEqual(result.Decision, original.Decision) {
		t.Fatalf("historical outcome was reevaluated: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("replay changed authority or publication")
	}
}

func changedProtected(t *testing.T, p contract.ProtectedContract, part string) contract.ProtectedContract {
	t.Helper()
	manifest, scope, covered := p.Manifest(), p.ScopeDigest(), p.CoveredInputs()
	switch part {
	case "manifest":
		var err error
		manifest, err = artifact.NewManifest([]artifact.Entry{{Path: "tests/other.txt", Content: artifact.Hash([]byte("other"))}})
		if err != nil {
			t.Fatal(err)
		}
	case "scope":
		scope = artifact.Hash([]byte("different scope"))
	case "context":
		covered["runner"] = "v2"
	}
	value, err := contract.NewProtectedContract(manifest, scope, covered)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPromoteRejectsOperationIdentityConflicts(t *testing.T) {
	changes := map[string]func(*PromoteRequest){
		"revision":          func(r *PromoteRequest) { r.Reference.RevisionID = "revision-2" },
		"carrier":           func(r *PromoteRequest) { r.Carrier = "carrier-2" },
		"version":           func(r *PromoteRequest) { r.NewVersionID = "version-2" },
		"assessment source": func(r *PromoteRequest) { r.AssessmentSource = "merged-2" },
	}
	for _, part := range []string{"manifest", "scope", "context"} {
		changes[part] = func(r *PromoteRequest) { r.Proposed = changedProtected(t, r.Proposed, part) }
	}
	for _, part := range []string{"project", "target", "source", "carrier", "kind"} {
		changes["integration "+part] = func(r *PromoteRequest) {
			i := r.Integration
			project, target, source, carrier, kind := i.ProjectID(), i.Target(), i.Source(), i.Carrier(), i.Kind()
			switch part {
			case "project":
				project = "other"
			case "target":
				target = "other"
			case "source":
				source = "other"
			case "carrier":
				carrier = "other"
			case "kind":
				kind, carrier = contract.IntegrationExistingBaseline, ""
			}
			var err error
			r.Integration, err = contract.NewIntegration(project, target, source, carrier, kind)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f, store := approvedStoreFixture(t)
			if _, err := Promote(context.Background(), store, f.request); err != nil {
				t.Fatal(err)
			}
			before := store.inspect()
			change(&f.request)
			result, err := Promote(context.Background(), store, f.request)
			if !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("changed identity adopted old operation: %+v %v", result, err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("conflict changed original receipt")
			}
		})
	}
	t.Run("consent operation", func(t *testing.T) {
		f, store := approvedStoreFixture(t)
		f.request.OperationID = f.command.OperationID()
		before := store.inspect()
		result, err := Promote(context.Background(), store, f.request)
		if !errors.Is(err, ErrOperationConflict) {
			t.Fatalf("consent operation reused: %+v %v", result, err)
		}
		requireZeroPromotionResult(t, result)
		if !reflect.DeepEqual(before, store.inspect()) {
			t.Fatal("kind conflict changed authority")
		}
	})
}

func TestPromoteRejectsMalformedHistoricalReceipt(t *testing.T) {
	for name, change := range map[string]func(*OperationReceipt){
		"kind":       func(r *OperationReceipt) { r.Kind = OperationKind(99) },
		"inner kind": func(r *OperationReceipt) { r.Promotion.Identity.Kind = OperationCorrect },
		"binding":    func(r *OperationReceipt) { r.Promotion.Identity.Binding = contract.ApprovalBinding{} },
		"reference":  func(r *OperationReceipt) { r.Promotion.Identity.Request.Reference.RevisionID = "contradictory" },
		"content": func(r *OperationReceipt) {
			r.Promotion.Identity.Request.Proposed = changedProtected(t, r.Promotion.Identity.Request.Proposed, "scope")
		},
		"version":   func(r *OperationReceipt) { r.Promotion.Identity.Request.NewVersionID = "contradictory" },
		"timestamp": func(r *OperationReceipt) { r.Promotion.Identity.Request.RecordedAt = time.Time{} },
		"decision":  func(r *OperationReceipt) { r.Promotion.Decision = contract.PromotionDecision{} },
	} {
		t.Run(name, func(t *testing.T) {
			f, store := approvedStoreFixture(t)
			if _, err := Promote(context.Background(), store, f.request); err != nil {
				t.Fatal(err)
			}
			before := store.inspect()
			result, err := Promote(context.Background(), changedPromotionLoad{Store: store, change: func(s *Snapshot) { change(&s.Operation) }}, f.request)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("malformed historical receipt accepted: %+v %v", result, err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("malformed replay wrote authority")
			}
		})
	}
}

func TestPromotionRejectsNilStore(t *testing.T) {
	f := newStoreFixture(t)
	for name, call := range map[string]func() (PromoteResult, error){
		"promote": func() (PromoteResult, error) { return Promote(context.Background(), nil, f.request) },
		"bootstrap": func() (PromoteResult, error) {
			return Bootstrap(context.Background(), nil, BootstrapRequest{Promotion: f.request})
		},
		"correct": func() (PromoteResult, error) {
			return Correct(context.Background(), nil, CorrectionRequest{Promotion: f.request, TargetVersionID: "version"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Errorf("nil store panicked instead of returning ErrInvalidRequest: %v", value)
				}
			}()
			result, err := call()
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("nil store error: %v", err)
			}
			requireZeroPromotionResult(t, result)
		})
	}
}

func TestPromotionRejectsMalformedReceiptEnvelope(t *testing.T) {
	for name, change := range map[string]func(*OperationReceipt){
		"absent kind with payload":       func(r *OperationReceipt) { r.Kind = 0 },
		"consent without command":        func(r *OperationReceipt) { *r = OperationReceipt{Kind: OperationConsent} },
		"consent with promotion payload": func(r *OperationReceipt) { r.Kind = OperationConsent },
		"integration carrier contradicts record": func(r *OperationReceipt) {
			i := r.Promotion.Identity.Request.Integration
			var err error
			r.Promotion.Identity.Request.Integration, err = contract.NewIntegration(i.ProjectID(), i.Target(), i.Source(), "contradictory", i.Kind())
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, store := approvedStoreFixture(t)
			if _, err := Promote(context.Background(), store, f.request); err != nil {
				t.Fatal(err)
			}
			before := store.inspect()
			result, err := Promote(context.Background(), changedPromotionLoad{Store: store, change: func(s *Snapshot) { change(&s.Operation) }}, f.request)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("malformed receipt envelope accepted: outcome=%v error=%v", result.Decision.Outcome(), err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("malformed receipt changed history")
			}
		})
	}
}

// changedPromotionLoad exercises defensive handling at the Store boundary,
// while all state and commit behavior still use the shared reference store.
type changedPromotionLoad struct {
	Store
	change func(*Snapshot)
}

func (s changedPromotionLoad) Load(ctx context.Context, request ReadRequest) (Snapshot, error) {
	value, err := s.Store.Load(ctx, request)
	if err == nil {
		s.change(&value)
	}
	return value, err
}

func requireZeroPromotionResult(t *testing.T, result PromoteResult) {
	t.Helper()
	if !reflect.DeepEqual(result, PromoteResult{}) {
		t.Fatalf("error exposed an outcome as success: %+v", result)
	}
}

func TestPromoteRejectsMalformedRequestBeforeLoading(t *testing.T) {
	for name, change := range map[string]func(*PromoteRequest){
		"operation":               func(r *PromoteRequest) { r.OperationID = "" },
		"blank operation":         func(r *PromoteRequest) { r.OperationID = "\t" },
		"project":                 func(r *PromoteRequest) { r.Reference.ProjectID = "" },
		"suite":                   func(r *PromoteRequest) { r.Reference.SuiteID = " " },
		"proposal":                func(r *PromoteRequest) { r.Reference.ProposalID = "" },
		"revision":                func(r *PromoteRequest) { r.Reference.RevisionID = "\n" },
		"carrier":                 func(r *PromoteRequest) { r.Carrier = "" },
		"blank carrier":           func(r *PromoteRequest) { r.Carrier = " \t" },
		"proposed":                func(r *PromoteRequest) { r.Proposed = contract.ProtectedContract{} },
		"assessment source":       func(r *PromoteRequest) { r.AssessmentSource = "" },
		"blank assessment source": func(r *PromoteRequest) { r.AssessmentSource = "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			f := approvedPromotionFixture(t)
			store := newReferenceStore(t, f.snapshot)
			store.failAt = "load"
			change(&f.request)
			result, err := Promote(context.Background(), store, f.request)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("malformed request reached authority loading: %v", err)
			}
			requireZeroPromotionResult(t, result)
		})
	}
}

func TestPromoteRejectsMalformedLoadedAuthority(t *testing.T) {
	for name, change := range map[string]func(*Snapshot){
		"canonical":      func(s *Snapshot) { s.Canonical = contract.CanonicalSnapshot{} },
		"fence project":  func(s *Snapshot) { s.Fence.ProjectID = "other" },
		"fence suite":    func(s *Snapshot) { s.Fence.SuiteID = "other" },
		"fence revision": func(s *Snapshot) { s.Fence.Revision++ },
		"proposal":       func(s *Snapshot) { s.Proposal = contract.Proposal{} },
		"policy":         func(s *Snapshot) { s.Policy = contract.Policy{} },
		"foreign policy": func(s *Snapshot) {
			owner, err := contract.NewPrincipal("owner", contract.Human)
			if err != nil {
				t.Fatal(err)
			}
			s.Policy, err = contract.NewPolicy("other", "policy", owner)
			if err != nil {
				t.Fatal(err)
			}
		},
		"schedule": func(s *Snapshot) { s.Scheduling = contract.Schedule{} },
		"foreign schedule": func(s *Snapshot) {
			value, err := contract.NewSchedule("project", "other")
			if err != nil {
				t.Fatal(err)
			}
			s.Scheduling = value
		},
		"target":       func(s *Snapshot) { s.Target = "" },
		"blank target": func(s *Snapshot) { s.Target = "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			f := approvedPromotionFixture(t)
			store := newReferenceStore(t, f.snapshot)
			before := store.inspect()
			result, err := Promote(context.Background(), changedPromotionLoad{Store: store, change: change}, f.request)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("malformed stored authority accepted: %v", err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("invalid snapshot caused a write")
			}
		})
	}
}

func TestPromoteProvisionalOutcomesNeverCommit(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	before := store.inspect()
	result, err := Promote(context.Background(), store, f.request)
	if err != nil || result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionBlocked || result.Decision.Reason() != contract.PromotionReasonApprovalMissing {
		t.Fatalf("missing consent was not provisional: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("blocked proposal changed authority")
	}
	f = approvedPromotionFixture(t)
	store = newReferenceStore(t, f.snapshot)
	if result, err := Promote(context.Background(), store, f.request); err != nil || !result.Committed {
		t.Fatalf("setup promotion failed: %+v %v", result, err)
	}
	before = store.inspect()
	f.request.OperationID = "no-change"
	f.request.NewVersionID = ""
	f.request.RecordedAt = time.Time{}
	result, err = Promote(context.Background(), store, f.request)
	if err != nil || result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionNoChange {
		t.Fatalf("unchanged contract was not provisional: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("no-change reserved an operation or advanced queue")
	}
}

func TestPromoteFailureNeverReportsOrExposesPartialCommit(t *testing.T) {
	for _, point := range []string{"load", "version", "pointer", "promotion", "receipt", "audit", "schedule", "publication"} {
		t.Run(point, func(t *testing.T) {
			f := approvedPromotionFixture(t)
			store := newReferenceStore(t, f.snapshot)
			before := store.inspect()
			store.failAt = point
			result, err := Promote(context.Background(), store, f.request)
			if !errors.Is(err, errReferenceFailure) {
				t.Fatalf("injected %s failure not returned: %v", point, err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatalf("failure at %s exposed partial authority or publication", point)
			}
			store.failAt = ""
			result, err = Promote(context.Background(), store, f.request)
			if err != nil || !result.Committed || result.Duplicate {
				t.Fatalf("retry after rollback failed: %+v %v", result, err)
			}
		})
	}
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

// Aggregate regressions below compose already proven application and Store
// behavior; they are not presented as new implementation RED checkpoints.
func TestPromoteGlobalReceiptSurvivesMissingAggregateAndRejectsForeignScope(t *testing.T) {
	f, store := approvedStoreFixture(t)
	original, err := Promote(context.Background(), store, f.request)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"project", "suite", "proposal"} {
		request := f.request
		switch part {
		case "project":
			request.Reference.ProjectID = "other"
		case "suite":
			request.Reference.SuiteID = "other"
		case "proposal":
			request.Reference.ProposalID = "missing"
		}
		result, err := Promote(context.Background(), store, request)
		if !errors.Is(err, ErrOperationConflict) {
			t.Fatalf("%s global identity conflict hidden: %v", part, err)
		}
		requireZeroPromotionResult(t, result)
	}
	state := store.inspect()
	err = store.updateAuthority(context.Background(), AuthorityFence{"project", "suite", state.canonical.Suite().Revision()}, func(s *referenceState) error {
		delete(s.proposals, f.request.Reference.ProposalID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := store.inspect()
	result, err := Promote(context.Background(), store, f.request)
	if err != nil || !result.Committed || !result.Duplicate || !reflect.DeepEqual(original.Decision, result.Decision) {
		t.Fatalf("missing live aggregate erased durable replay: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("historical replay wrote state")
	}
}

func TestPromoteDomainErrorAndHistoricalVersionCollisionLeaveNoEffects(t *testing.T) {
	for name, change := range map[string]func(*PromoteRequest){
		"absent version":   func(r *PromoteRequest) { r.NewVersionID = "" },
		"blank version":    func(r *PromoteRequest) { r.NewVersionID = " " },
		"absent timestamp": func(r *PromoteRequest) { r.RecordedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			f, store := approvedStoreFixture(t)
			before := store.inspect()
			change(&f.request)
			result, err := Promote(context.Background(), store, f.request)
			if !errors.Is(err, contract.ErrInvalidPromotion) {
				t.Fatalf("domain error hidden: %v", err)
			}
			requireZeroPromotionResult(t, result)
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("malformed effect wrote state")
			}
		})
	}
	store, correction := correctionFixture(t)
	request := correction.Promotion
	request.NewVersionID = correction.TargetVersionID
	before := store.inspect()
	result, err := Promote(context.Background(), store, request)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("historical logical version reused: %+v %v", result, err)
	}
	requireZeroPromotionResult(t, result)
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("version collision reserved identities or changed history")
	}
}
