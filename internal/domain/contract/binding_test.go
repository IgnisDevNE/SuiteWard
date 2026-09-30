package contract_test

import (
	"maps"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func proposalBindingInput() contract.BindingInput {
	return contract.BindingInput{
		Reference:         contract.ProposalReference{ProjectID: "project-1", SuiteID: "suite-1", ProposalID: "proposal-1", RevisionID: "revision-1"},
		ExpectedCanonical: "version-1",
		Manifest:          artifact.Hash([]byte("manifest")),
		Scope:             artifact.Hash([]byte("scope")),
		PolicyRevision:    "policy-1",
		CoveredInputs:     map[string]string{"runner": "runner-1", "environment": "environment-1"},
	}
}

func mustBinding(t *testing.T, input contract.BindingInput) contract.ApprovalBinding {
	t.Helper()
	binding, err := contract.NewApprovalBinding(input)
	if err != nil {
		t.Fatalf("NewApprovalBinding(): %v", err)
	}
	return binding
}

func TestApprovalBindingPreservesCoveredInputs(t *testing.T) {
	input := proposalBindingInput()
	binding := mustBinding(t, input)
	if binding.IsZero() || binding.Reference() != input.Reference || binding.ExpectedCanonical() != input.ExpectedCanonical || binding.ManifestDigest() != input.Manifest || binding.ScopeDigest() != input.Scope || binding.PolicyRevisionID() != input.PolicyRevision || !maps.Equal(binding.CoveredInputs(), input.CoveredInputs) {
		t.Fatal("binding did not preserve its exact supplied covered inputs")
	}
	input.CoveredInputs["runner"] = "changed"
	returned := binding.CoveredInputs()
	if returned == nil {
		t.Fatal("binding lost its covered-input map")
	}
	returned["environment"] = "changed"
	delete(returned, "runner")
	if !maps.Equal(binding.CoveredInputs(), proposalBindingInput().CoveredInputs) {
		t.Fatal("caller-owned maps changed an existing binding")
	}
}

func TestApprovalBindingEqualityRequiresEveryExactInput(t *testing.T) {
	base := mustBinding(t, proposalBindingInput())
	if !base.Equal(mustBinding(t, proposalBindingInput())) {
		t.Fatal("identical covered inputs must compare equal")
	}
	cases := []struct {
		name   string
		change func(*contract.BindingInput)
	}{
		{"project", func(i *contract.BindingInput) { i.Reference.ProjectID = "project-2" }},
		{"suite", func(i *contract.BindingInput) { i.Reference.SuiteID = "suite-2" }},
		{"proposal", func(i *contract.BindingInput) { i.Reference.ProposalID = "proposal-2" }},
		{"revision", func(i *contract.BindingInput) { i.Reference.RevisionID = "revision-2" }},
		{"baseline", func(i *contract.BindingInput) { i.ExpectedCanonical = "version-2" }},
		{"absent baseline", func(i *contract.BindingInput) { i.ExpectedCanonical = "" }},
		{"manifest", func(i *contract.BindingInput) { i.Manifest = artifact.Hash([]byte("other manifest")) }},
		{"scope", func(i *contract.BindingInput) { i.Scope = artifact.Hash([]byte("other scope")) }},
		{"policy", func(i *contract.BindingInput) { i.PolicyRevision = "policy-2" }},
		{"context value", func(i *contract.BindingInput) { i.CoveredInputs["runner"] = "runner-2" }},
		{"context addition", func(i *contract.BindingInput) { i.CoveredInputs["explicit-source"] = "source-1" }},
		{"context removal", func(i *contract.BindingInput) { delete(i.CoveredInputs, "runner") }},
		{"context key", func(i *contract.BindingInput) {
			i.CoveredInputs["Runner"] = i.CoveredInputs["runner"]
			delete(i.CoveredInputs, "runner")
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			input := proposalBindingInput()
			tt.change(&input)
			changed := mustBinding(t, input)
			if base.Equal(changed) || changed.Equal(base) {
				t.Fatal("different covered inputs must not compare equal")
			}
		})
	}
}

func TestApprovalBindingEmptyContextAndAbsentValues(t *testing.T) {
	input := proposalBindingInput()
	input.ExpectedCanonical = ""
	input.CoveredInputs = nil
	base := mustBinding(t, input)
	input.CoveredInputs = map[string]string{}
	if !base.Equal(mustBinding(t, input)) || base.ExpectedCanonical() != "" {
		t.Fatal("nil/empty context must describe the same set with explicit absent baseline")
	}
	var absent contract.ApprovalBinding
	if !absent.IsZero() || absent.Equal(absent) || absent.Equal(base) || base.Equal(absent) {
		t.Fatal("absent bindings must not match usable consent or evidence")
	}
	input = proposalBindingInput()
	input.CoveredInputs = map[string]string{"environment": "environment-1", "runner": "runner-1"}
	if !mustBinding(t, input).Equal(mustBinding(t, proposalBindingInput())) {
		t.Fatal("map insertion order changed binding identity")
	}
}
