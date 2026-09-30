package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestIntegrityEvidencePreservesObservation(t *testing.T) {
	input := integrityBindingInput()
	binding := mustIntegrityBinding(t, input)
	for _, tt := range []struct {
		name    string
		outcome contract.IntegrityOutcome
	}{
		{name: "passed", outcome: contract.IntegrityPassed},
		{name: "failed", outcome: contract.IntegrityFailed},
		{name: "unavailable", outcome: contract.IntegrityUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evidence, err := contract.NewIntegrityEvidence(" verifier-1 ", " source-1 ", binding, tt.outcome)
			if err != nil {
				t.Fatalf("NewIntegrityEvidence() error = %v", err)
			}
			if evidence.IsZero() {
				t.Error("constructed observation is reported as absent")
			}
			if evidence.EmitterID() != " verifier-1 " || evidence.Source() != " source-1 " {
				t.Errorf("observation identity = (%q, %q), want unchanged emitter and source", evidence.EmitterID(), evidence.Source())
			}
			if !evidence.Binding().Equal(binding) || evidence.Outcome() != tt.outcome {
				t.Errorf("observation did not preserve the exact binding and outcome %v", tt.outcome)
			}
		})
	}
}

func TestIntegrityEvidenceKeepsCoveredInputsImmutable(t *testing.T) {
	input := integrityBindingInput()
	binding := mustIntegrityBinding(t, input)
	evidence, err := contract.NewIntegrityEvidence("verifier-1", "source-1", binding, contract.IntegrityPassed)
	if err != nil {
		t.Fatalf("NewIntegrityEvidence() error = %v", err)
	}
	input.CoveredInputs["dependencies"] = "changed-by-caller"
	exposed := evidence.Binding().CoveredInputs()
	if exposed["dependencies"] != "lock-1" {
		t.Fatalf("retained context = %v, want original dependencies", exposed)
	}
	exposed["dependencies"] = "changed-through-getter"
	delete(exposed, "mode")
	if !evidence.Binding().Equal(binding) || evidence.Binding().CoveredInputs()["dependencies"] != "lock-1" {
		t.Error("caller mutation changed the evidence's sealed context")
	}
}

func TestIntegrityEvidenceRejectsInvalidObservation(t *testing.T) {
	binding := mustIntegrityBinding(t, integrityBindingInput())
	tests := []struct {
		name    string
		emitter contract.PrincipalID
		source  contract.SourceRevision
		binding contract.ApprovalBinding
		outcome contract.IntegrityOutcome
	}{
		{name: "missing emitter", source: "source-1", binding: binding, outcome: contract.IntegrityPassed},
		{name: "blank emitter", emitter: " \t", source: "source-1", binding: binding, outcome: contract.IntegrityPassed},
		{name: "missing source", emitter: "verifier-1", binding: binding, outcome: contract.IntegrityPassed},
		{name: "blank source", emitter: "verifier-1", source: "\u2003", binding: binding, outcome: contract.IntegrityPassed},
		{name: "absent binding", emitter: "verifier-1", source: "source-1", outcome: contract.IntegrityPassed},
		{name: "zero outcome", emitter: "verifier-1", source: "source-1", binding: binding},
		{name: "unknown outcome", emitter: "verifier-1", source: "source-1", binding: binding, outcome: 255},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := contract.NewIntegrityEvidence(tt.emitter, tt.source, tt.binding, tt.outcome)
			if !errors.Is(err, contract.ErrInvalidIntegrityEvidence) {
				t.Fatalf("NewIntegrityEvidence() error = %v, want ErrInvalidIntegrityEvidence", err)
			}
		})
	}
}

func TestIntegrityEvidenceZeroIsAbsent(t *testing.T) {
	var evidence contract.IntegrityEvidence
	if !evidence.IsZero() || evidence.EmitterID() != "" || evidence.Source() != "" || !evidence.Binding().IsZero() || evidence.Outcome() != 0 {
		t.Error("zero evidence unexpectedly describes an observation")
	}
}

func integrityBindingInput() contract.BindingInput {
	return contract.BindingInput{
		Reference: contract.ProposalReference{
			ProjectID: "project-1", SuiteID: "suite-1", ProposalID: "proposal-1", RevisionID: "revision-1",
		},
		ExpectedCanonical: "version-1",
		Manifest:          artifact.Hash([]byte("manifest-1")),
		Scope:             artifact.Hash([]byte("scope-1")),
		PolicyRevision:    "policy-1",
		CoveredInputs:     map[string]string{"mode": "integrity", "dependencies": "lock-1"},
	}
}

func mustIntegrityBinding(t *testing.T, input contract.BindingInput) contract.ApprovalBinding {
	t.Helper()
	binding, err := contract.NewApprovalBinding(input)
	if err != nil {
		t.Fatalf("NewApprovalBinding() error = %v", err)
	}
	return binding
}
