package contract_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestIntegrityAssessmentReportsObservation(t *testing.T) {
	binding := mustIntegrityBinding(t, integrityBindingInput())
	source := contract.SourceRevision(" source-1 ")
	passed := mustIntegrityEvidence(t, source, binding, contract.IntegrityPassed)
	failed := mustIntegrityEvidence(t, source, binding, contract.IntegrityFailed)
	unavailable := mustIntegrityEvidence(t, source, binding, contract.IntegrityUnavailable)
	tests := []struct {
		name     string
		evidence *contract.IntegrityEvidence
		reason   contract.IntegrityReason
		passed   bool
		present  bool
	}{
		{name: "missing", reason: contract.IntegrityReasonMissing},
		{name: "unconstructed", evidence: &contract.IntegrityEvidence{}, reason: contract.IntegrityReasonMissing},
		{name: "passed", evidence: &passed, reason: contract.IntegrityReasonSatisfied, passed: true, present: true},
		{name: "failed", evidence: &failed, reason: contract.IntegrityReasonFailed, present: true},
		{name: "unavailable", evidence: &unavailable, reason: contract.IntegrityReasonUnavailable, present: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assessment, err := contract.AssessIntegrity(source, binding, tt.evidence)
			if err != nil {
				t.Fatalf("AssessIntegrity() error = %v", err)
			}
			if assessment.Reason() != tt.reason || assessment.Passed() != tt.passed {
				t.Errorf("assessment = (reason %v, passed %t), want (%v, %t)", assessment.Reason(), assessment.Passed(), tt.reason, tt.passed)
			}
			if assessment.Source() != source || !assessment.Binding().Equal(binding) {
				t.Error("assessment did not retain the exact expected source and binding")
			}
			if assessment.Assurance() != contract.IntegrityOnly {
				t.Error("constructed assessment did not identify integrity-only assurance")
			}
			observed, present := assessment.Evidence()
			if present != tt.present {
				t.Errorf("evidence presence = %t, want %t", present, tt.present)
			}
			if tt.present {
				if observed.IsZero() || observed.EmitterID() != tt.evidence.EmitterID() || observed.Source() != source || !observed.Binding().Equal(binding) || observed.Outcome() != tt.evidence.Outcome() {
					t.Error("assessment did not retain the supplied observation")
				}
			} else if !observed.IsZero() {
				t.Error("missing evidence unexpectedly returned an observation")
			}
		})
	}
}

func TestIntegrityAssessmentZeroFailsClosed(t *testing.T) {
	var assessment contract.IntegrityAssessment
	if assessment.Passed() || assessment.Reason() != 0 || assessment.Assurance() != 0 {
		t.Error("zero assessment unexpectedly claims a result or assurance")
	}
	if assessment.Source() != "" || !assessment.Binding().IsZero() {
		t.Error("zero assessment unexpectedly has an evaluation context")
	}
	if evidence, present := assessment.Evidence(); present || !evidence.IsZero() {
		t.Error("zero assessment unexpectedly has evidence")
	}
}

func TestIntegrityAssessmentKeepsSnapshotImmutable(t *testing.T) {
	input := integrityBindingInput()
	binding := mustIntegrityBinding(t, input)
	evidence := mustIntegrityEvidence(t, "source-1", binding, contract.IntegrityPassed)
	assessment, err := contract.AssessIntegrity("source-1", binding, &evidence)
	if err != nil {
		t.Fatalf("AssessIntegrity() error = %v", err)
	}
	evidence = mustIntegrityEvidence(t, "source-2", binding, contract.IntegrityFailed)
	input.CoveredInputs["dependencies"] = "caller-replaced-context"
	expectedContext := assessment.Binding().CoveredInputs()
	if expectedContext["dependencies"] != "lock-1" {
		t.Fatalf("expected context = %v, want unchanged context", expectedContext)
	}
	expectedContext["dependencies"] = "mutated-getter-context"
	retained, present := assessment.Evidence()
	if !present {
		t.Fatal("assessment lost its constructed observation")
	}
	observedContext := retained.Binding().CoveredInputs()
	observedContext["dependencies"] = "mutated-observation-context"
	retained, present = assessment.Evidence()
	if !assessment.Passed() || assessment.Source() != "source-1" || !assessment.Binding().Equal(binding) || !present || retained.Source() != "source-1" || retained.Outcome() != contract.IntegrityPassed || !retained.Binding().Equal(binding) {
		t.Error("caller mutation changed the assessment's expected or observed facts")
	}
}

func TestIntegrityAssessmentRejectsMismatchedEvidence(t *testing.T) {
	expected := mustIntegrityBinding(t, integrityBindingInput())
	changes := []struct {
		name   string
		change func(*contract.BindingInput, *contract.SourceRevision)
	}{
		{name: "source revision", change: func(_ *contract.BindingInput, source *contract.SourceRevision) { *source = "source-2" }},
		{name: "project", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.Reference.ProjectID = "project-2"
		}},
		{name: "suite", change: func(input *contract.BindingInput, _ *contract.SourceRevision) { input.Reference.SuiteID = "suite-2" }},
		{name: "proposal", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.Reference.ProposalID = "proposal-2"
		}},
		{name: "proposal revision", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.Reference.RevisionID = "revision-2"
		}},
		{name: "canonical version", change: func(input *contract.BindingInput, _ *contract.SourceRevision) { input.ExpectedCanonical = "version-2" }},
		{name: "canonical absence", change: func(input *contract.BindingInput, _ *contract.SourceRevision) { input.ExpectedCanonical = "" }},
		{name: "manifest", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.Manifest = artifact.Hash([]byte("manifest-2"))
		}},
		{name: "scope", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.Scope = artifact.Hash([]byte("scope-2"))
		}},
		{name: "governing policy", change: func(input *contract.BindingInput, _ *contract.SourceRevision) { input.PolicyRevision = "policy-2" }},
		{name: "covered input changed", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.CoveredInputs["dependencies"] = "lock-2"
		}},
		{name: "covered input added", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			input.CoveredInputs["new-input"] = "new-value"
		}},
		{name: "covered input removed", change: func(input *contract.BindingInput, _ *contract.SourceRevision) {
			delete(input.CoveredInputs, "dependencies")
		}},
	}
	for _, change := range changes {
		for _, outcome := range []struct {
			name  string
			value contract.IntegrityOutcome
		}{
			{name: "passed", value: contract.IntegrityPassed},
			{name: "failed", value: contract.IntegrityFailed},
			{name: "unavailable", value: contract.IntegrityUnavailable},
		} {
			t.Run(change.name+"/"+outcome.name, func(t *testing.T) {
				input := integrityBindingInput()
				source := contract.SourceRevision("source-1")
				change.change(&input, &source)
				observedBinding := mustIntegrityBinding(t, input)
				evidence := mustIntegrityEvidence(t, source, observedBinding, outcome.value)
				assessment, err := contract.AssessIntegrity("source-1", expected, &evidence)
				if err != nil {
					t.Fatalf("AssessIntegrity() error = %v", err)
				}
				if assessment.Passed() || assessment.Reason() != contract.IntegrityReasonMismatch {
					t.Errorf("mismatched evidence produced (passed %t, reason %v), want (false, mismatch)", assessment.Passed(), assessment.Reason())
				}
				if assessment.Source() != "source-1" || !assessment.Binding().Equal(expected) || assessment.Assurance() != contract.IntegrityOnly {
					t.Error("mismatch changed the expected assessment context or assurance")
				}
				stored, present := assessment.Evidence()
				if !present || stored.Source() != source || !stored.Binding().Equal(observedBinding) || stored.Outcome() != outcome.value {
					t.Error("mismatch did not preserve the actual supplied evidence")
				}
			})
		}
	}
}

func mustIntegrityEvidence(t *testing.T, source contract.SourceRevision, binding contract.ApprovalBinding, outcome contract.IntegrityOutcome) contract.IntegrityEvidence {
	t.Helper()
	evidence, err := contract.NewIntegrityEvidence("verifier-1", source, binding, outcome)
	if err != nil {
		t.Fatalf("NewIntegrityEvidence() error = %v", err)
	}
	return evidence
}
