package contract_test

import (
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
