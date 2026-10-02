package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func addAssessment(t *testing.T, store *referenceStore, reference contract.ProposalReference, source contract.SourceRevision, binding contract.ApprovalBinding) {
	t.Helper()
	evidence, err := contract.NewIntegrityEvidence("verifier", source, binding, contract.IntegrityPassed)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := contract.AssessIntegrity(source, binding, &evidence)
	if err != nil {
		t.Fatal(err)
	}
	state := store.inspect()
	err = store.updateAuthority(context.Background(), AuthorityFence{"project", "suite", state.canonical.Suite().Revision()}, func(s *referenceState) error {
		s.assessments[assessmentKey{reference, source}] = assessment
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapBothModesCommitAndReplay(t *testing.T) {
	for _, mode := range []contract.BootstrapMode{contract.ExistingBaselineBootstrap, contract.FirstTestBootstrap} {
		t.Run(map[contract.BootstrapMode]string{contract.ExistingBaselineBootstrap: "existing", contract.FirstTestBootstrap: "first tests"}[mode], func(t *testing.T) {
			f, store := approvedStoreFixture(t)
			if mode == contract.ExistingBaselineBootstrap {
				f.request.AssessmentSource = f.snapshot.Proposal.Current().Origin()
				var err error
				f.request.Integration, err = contract.NewIntegration("project", "default", f.request.AssessmentSource, "", contract.IntegrationExistingBaseline)
				if err != nil {
					t.Fatal(err)
				}
				addAssessment(t, store, f.request.Reference, f.request.AssessmentSource, f.snapshot.Proposal.Current().Binding())
			}
			before := store.inspect()
			request := BootstrapRequest{Mode: mode, Promotion: f.request}
			result, err := Bootstrap(context.Background(), store, request)
			if err != nil || !result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionProposed {
				t.Fatalf("bootstrap not committed: %+v %v", result, err)
			}
			after := store.inspect()
			if after.canonical.Version().ID() != f.request.NewVersionID || len(after.publications) != len(before.publications)+1 || after.scheduling.Entries()[0].State() != contract.SchedulePromoted {
				t.Fatal("bootstrap effect incomplete")
			}
			receipt := after.operations[f.request.OperationID].Promotion
			if receipt.Identity.Kind != OperationBootstrap || receipt.Identity.BootstrapMode != mode {
				t.Fatal("bootstrap identity lost")
			}
			request.Promotion.RecordedAt = request.Promotion.RecordedAt.Add(time.Hour)
			replay, err := Bootstrap(context.Background(), store, request)
			if err != nil || !replay.Committed || !replay.Duplicate || !reflect.DeepEqual(replay.Decision, result.Decision) || !reflect.DeepEqual(after, store.inspect()) {
				t.Fatalf("bootstrap replay changed original: %+v %v", replay, err)
			}
			other, err := Promote(context.Background(), store, request.Promotion)
			if !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("bootstrap receipt adopted by ordinary promotion: %+v %v", other, err)
			}
			requireZeroPromotionResult(t, other)
			if mode == contract.FirstTestBootstrap {
				request.Mode = contract.ExistingBaselineBootstrap
			} else {
				request.Mode = contract.FirstTestBootstrap
			}
			other, err = Bootstrap(context.Background(), store, request)
			if !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("changed bootstrap mode replayed: %+v %v", other, err)
			}
			requireZeroPromotionResult(t, other)
		})
	}
}

func TestBootstrapPremergeReadinessAndFailuresRemainProvisional(t *testing.T) {
	f, store := approvedStoreFixture(t)
	request := BootstrapRequest{Mode: contract.FirstTestBootstrap, Promotion: f.request}
	request.Promotion.Integration = contract.Integration{}
	request.Promotion.AssessmentSource = f.snapshot.Proposal.Current().Origin()
	request.Promotion.NewVersionID = ""
	request.Promotion.RecordedAt = time.Time{}
	addAssessment(t, store, f.request.Reference, request.Promotion.AssessmentSource, f.snapshot.Proposal.Current().Binding())
	before := store.inspect()
	result, err := Bootstrap(context.Background(), store, request)
	if err != nil || result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionReady {
		t.Fatalf("premerge readiness wrong: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("premerge readiness wrote authority")
	}
	request.Promotion.Integration = f.request.Integration
	result, err = Bootstrap(context.Background(), store, request)
	if err != nil || result.Committed || result.Decision.Reason() != contract.PromotionReasonAssessmentMismatch {
		t.Fatalf("candidate evidence authorized merged source: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("mismatched evidence wrote authority")
	}
	request.Mode = contract.ExistingBaselineBootstrap
	result, err = Bootstrap(context.Background(), store, request)
	if err != nil || result.Committed || result.Decision.Reason() != contract.PromotionReasonIntegrationMismatch {
		t.Fatalf("wrong integration kind accepted: %+v %v", result, err)
	}
	request = BootstrapRequest{Mode: contract.FirstTestBootstrap, Promotion: f.request}
	if _, err = Bootstrap(context.Background(), store, request); err != nil {
		t.Fatal(err)
	}
	request.Promotion.OperationID = "another-bootstrap"
	before = store.inspect()
	result, err = Bootstrap(context.Background(), store, request)
	if err != nil || result.Committed || result.Decision.Reason() != contract.PromotionReasonCanonicalPresent {
		t.Fatalf("existing canonical bootstrapped again: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("second bootstrap changed authority")
	}
}

func TestBootstrapInvalidModeAndStoreFailure(t *testing.T) {
	f, store := approvedStoreFixture(t)
	store.failAt = "load"
	for _, mode := range []contract.BootstrapMode{0, 99} {
		result, err := Bootstrap(context.Background(), store, BootstrapRequest{Mode: mode, Promotion: f.request})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid bootstrap mode reached store: %+v %v", result, err)
		}
		requireZeroPromotionResult(t, result)
	}
	result, err := Bootstrap(context.Background(), store, BootstrapRequest{Mode: contract.FirstTestBootstrap, Promotion: f.request})
	if !errors.Is(err, errReferenceFailure) {
		t.Fatalf("load failure hidden: %v", err)
	}
	requireZeroPromotionResult(t, result)
}

// appendApprovedCandidate uses real immutable constructors and transactional
// fixture writers; it does not fabricate an already-active competing proposal.
func appendApprovedCandidate(t *testing.T, store *referenceStore, id string, protected contract.ProtectedContract) PromoteRequest {
	t.Helper()
	state := store.inspect()
	ref := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: contract.ProposalID(id), RevisionID: "revision-1"}
	baseline, _ := state.canonical.Suite().CurrentVersionID()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: ref, ExpectedCanonical: baseline, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: state.policy.RevisionID(), CoveredInputs: protected.CoveredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	carrier := contract.ApprovalCarrierID("carrier-" + id)
	source := contract.SourceRevision("merged-" + id)
	revision, err := contract.NewProposalRevision(binding, contract.SourceRevision("candidate-"+id), carrier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := contract.NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := contract.NewConsent("project", "suite", ref.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	err = store.updateAuthority(context.Background(), AuthorityFence{"project", "suite", state.canonical.Suite().Revision()}, func(s *referenceState) error {
		s.proposals[ref.ProposalID] = proposal
		s.consents[ref.ProposalID] = consent
		var err error
		s.scheduling, err = s.scheduling.Admit(proposal, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	addAssessment(t, store, ref, source, binding)
	owner, err := contract.NewPrincipal(state.policy.OwnerID(), contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("approve-" + id), SourceCommandID: contract.SourceCommandID("comment-" + id), Actor: owner, Reference: ref, Carrier: carrier, Action: contract.ApproveConsent, Order: 1})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(context.Background(), ReadRequest{Reference: ref, AssessmentSource: source})
	if err != nil {
		t.Fatal(err)
	}
	f := storeFixture{snapshot: snapshot, command: command}
	if err = store.CommitConsent(context.Background(), snapshot.Fence, f.consentWrite(t)); err != nil {
		t.Fatal(err)
	}
	integration, err := contract.NewIntegration("project", state.target, source, carrier, contract.IntegrationMergedChange)
	if err != nil {
		t.Fatal(err)
	}
	return PromoteRequest{OperationID: contract.OperationID("promote-" + id), Reference: ref, Carrier: carrier, Proposed: protected, AssessmentSource: source, Integration: integration, NewVersionID: contract.SuiteVersionID("version-" + id), RecordedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
}

func correctionFixture(t *testing.T) (*referenceStore, CorrectionRequest) {
	t.Helper()
	f, store := approvedStoreFixture(t)
	if result, err := Promote(context.Background(), store, f.request); err != nil || !result.Committed {
		t.Fatalf("initial promotion: %+v %v", result, err)
	}
	second := appendApprovedCandidate(t, store, "second", changedProtected(t, f.proposed, "context"))
	if result, err := Promote(context.Background(), store, second); err != nil || !result.Committed {
		t.Fatalf("second promotion: %+v %v", result, err)
	}
	correction := appendApprovedCandidate(t, store, "correction", f.proposed)
	return store, CorrectionRequest{Promotion: correction, TargetVersionID: f.request.NewVersionID}
}

func TestCorrectLoadsHistoryCommitsFreshVersionAndReplays(t *testing.T) {
	store, request := correctionFixture(t)
	before := store.inspect()
	result, err := Correct(context.Background(), store, request)
	if err != nil || !result.Committed || result.Duplicate {
		t.Fatalf("correction not committed: %+v %v", result, err)
	}
	effect, ok := result.Decision.Effect()
	if !ok || effect.Promotion().CorrectsVersionID() != request.TargetVersionID || effect.Version().ID() != request.Promotion.NewVersionID {
		t.Fatal("correction lost fresh version or target attribution")
	}
	after := store.inspect()
	if len(after.versions) != len(before.versions)+1 || len(after.publications) != len(before.publications)+1 || after.canonical.Version().ID() != request.Promotion.NewVersionID {
		t.Fatal("correction effects incomplete")
	}
	for id, version := range before.versions {
		if !reflect.DeepEqual(version, after.versions[id]) || !reflect.DeepEqual(before.promotions[id], after.promotions[id]) {
			t.Fatal("correction replaced immutable history")
		}
	}
	request.Promotion.RecordedAt = request.Promotion.RecordedAt.Add(time.Hour)
	replay, err := Correct(context.Background(), store, request)
	if err != nil || !replay.Committed || !replay.Duplicate || !reflect.DeepEqual(result.Decision, replay.Decision) || !reflect.DeepEqual(after, store.inspect()) {
		t.Fatalf("correction replay reevaluated: %+v %v", replay, err)
	}
	request.TargetVersionID = "version-second"
	result, err = Correct(context.Background(), store, request)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed correction target adopted old receipt: %+v %v", result, err)
	}
	requireZeroPromotionResult(t, result)
}

func TestCorrectRejectsMissingAndInconsistentHistory(t *testing.T) {
	store, request := correctionFixture(t)
	for _, target := range []contract.SuiteVersionID{"", " \n"} {
		invalid := request
		invalid.TargetVersionID = target
		result, err := Correct(context.Background(), store, invalid)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("empty target accepted: %+v %v", result, err)
		}
		requireZeroPromotionResult(t, result)
	}
	unknown := request
	unknown.TargetVersionID = "unknown"
	before := store.inspect()
	result, err := Correct(context.Background(), store, unknown)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing history not reported: %+v %v", result, err)
	}
	requireZeroPromotionResult(t, result)
	other, err := contract.NewHistoricalCanonical(before.versions["version-second"], before.promotions["version-second"])
	if err != nil {
		t.Fatal(err)
	}
	result, err = Correct(context.Background(), changedPromotionLoad{Store: store, change: func(s *Snapshot) { s.History = other }}, request)
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("wrong stored history accepted: %+v %v", result, err)
	}
	requireZeroPromotionResult(t, result)
	if !reflect.DeepEqual(before, store.inspect()) {
		t.Fatal("invalid correction changed state")
	}
}

func TestBootstrapAndCorrectionFailuresRollBackEveryEffect(t *testing.T) {
	for _, kind := range []string{"bootstrap", "correction"} {
		for _, point := range []string{"load", "version", "pointer", "promotion", "receipt", "audit", "schedule", "publication"} {
			t.Run(kind+"/"+point, func(t *testing.T) {
				var store *referenceStore
				var call func() (PromoteResult, error)
				if kind == "bootstrap" {
					f, s := approvedStoreFixture(t)
					store = s
					call = func() (PromoteResult, error) {
						return Bootstrap(context.Background(), store, BootstrapRequest{Mode: contract.FirstTestBootstrap, Promotion: f.request})
					}
				} else {
					s, request := correctionFixture(t)
					store = s
					call = func() (PromoteResult, error) { return Correct(context.Background(), store, request) }
				}
				before := store.inspect()
				store.failAt = point
				result, err := call()
				if !errors.Is(err, errReferenceFailure) {
					t.Fatalf("failure not surfaced: %v", err)
				}
				requireZeroPromotionResult(t, result)
				if !reflect.DeepEqual(before, store.inspect()) {
					t.Fatal("failure exposed partial canonical/history/audit/receipt/queue/publication")
				}
				store.failAt = ""
				result, err = call()
				if err != nil || !result.Committed || result.Duplicate {
					t.Fatalf("same identity retry after rollback failed: %+v %v", result, err)
				}
			})
		}
	}
}

func TestCorrectRevalidatesLoadedConsentEvidenceAndFreshVersion(t *testing.T) {
	for _, gate := range []string{"consent", "evidence", "current version", "target version"} {
		t.Run(gate, func(t *testing.T) {
			store, request := correctionFixture(t)
			var wantReason contract.PromotionReason
			var wantError error
			switch gate {
			case "consent", "evidence":
				state := store.inspect()
				err := store.updateAuthority(context.Background(), AuthorityFence{"project", "suite", state.canonical.Suite().Revision()}, func(s *referenceState) error {
					if gate == "consent" {
						s.consents[request.Promotion.Reference.ProposalID] = contract.Consent{}
					} else {
						delete(s.assessments, assessmentKey{request.Promotion.Reference, request.Promotion.AssessmentSource})
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if gate == "consent" {
					wantReason = contract.PromotionReasonApprovalMissing
				} else {
					wantReason = contract.PromotionReasonIntegrityNotPassed
				}
			case "current version":
				request.Promotion.NewVersionID = "version-second"
				wantError = contract.ErrInvalidCorrection
			case "target version":
				request.Promotion.NewVersionID = request.TargetVersionID
				wantError = contract.ErrInvalidCorrection
			}
			before := store.inspect()
			result, err := Correct(context.Background(), store, request)
			if wantError != nil {
				if !errors.Is(err, wantError) {
					t.Fatalf("fresh version rule bypassed: %v", err)
				}
				requireZeroPromotionResult(t, result)
			} else if err != nil || result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionBlocked || result.Decision.Reason() != wantReason {
				t.Fatalf("loaded authority bypassed: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, store.inspect()) {
				t.Fatal("invalid correction changed authority")
			}
		})
	}
}
