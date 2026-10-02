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
