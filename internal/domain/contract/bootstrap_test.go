package contract_test

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func lifecycleProtected(t *testing.T, manifest artifact.Manifest) contract.ProtectedContract {
	t.Helper()
	protected, err := contract.NewProtectedContract(manifest, artifact.Hash([]byte("scope")), map[string]string{"config": "v1"})
	if err != nil {
		t.Fatal(err)
	}
	return protected
}

func lifecycleAbsent(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	suite, err := contract.NewSuite("project", "suite", "", 7)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func lifecycleContext(t *testing.T, canonical contract.CanonicalSnapshot, manifest artifact.Manifest, proposalID contract.ProposalID, carrier contract.ApprovalCarrierID, origin, assessedSource contract.SourceRevision) contract.PromotionContext {
	t.Helper()
	protected := lifecycleProtected(t, manifest)
	baseline, _ := canonical.Suite().CurrentVersionID()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{
		Reference:         contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: proposalID, RevisionID: "revision-1"},
		ExpectedCanonical: baseline, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := contract.NewProposalRevision(binding, origin, carrier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := contract.NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := contract.NewPrincipal("owner", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := contract.NewPolicy("project", "policy", owner)
	if err != nil {
		t.Fatal(err)
	}
	consent, err := contract.NewConsent("project", "suite", proposalID)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.NewCommand(contract.CommandInput{
		OperationID: "approve-operation", SourceCommandID: "approve-source", Actor: owner,
		Reference: binding.Reference(), Carrier: carrier, Action: contract.ApproveConsent, Order: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	consent, result, err := consent.Apply(proposal, policy, command)
	if err != nil || result.Outcome() != contract.ConsentApproved {
		t.Fatalf("approve fixture: result=%v error=%v", result.Outcome(), err)
	}
	evidence, err := contract.NewIntegrityEvidence("observer", assessedSource, binding, contract.IntegrityPassed)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := contract.AssessIntegrity(assessedSource, binding, &evidence)
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
	return contract.PromotionContext{
		Canonical: canonical, Proposed: protected, Proposal: proposal, Reference: binding.Reference(), Carrier: carrier,
		Policy: policy, Consent: consent, Assessment: assessment, Scheduling: schedule,
		ExpectedStateRevision: canonical.Suite().Revision(), ExpectedSchedulingGeneration: schedule.Generation(),
	}
}

func lifecycleInput(context contract.PromotionContext) contract.PromotionInput {
	return contract.PromotionInput{
		Context: context, Target: "main", OperationID: "promote-operation", NewVersionID: "new-version",
		RecordedAt: time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC),
	}
}

func TestBootstrapFirstTestsReadinessHasNoCanonicalEffect(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	context := lifecycleContext(t, lifecycleAbsent(t), manifest, "first-tests", "first-pr", "candidate", "candidate")
	decision, err := contract.DecideBootstrap(contract.BootstrapInput{
		Mode: contract.FirstTestBootstrap, Promotion: contract.PromotionInput{Context: context},
	})
	if err != nil || decision.Outcome() != contract.PromotionReady || decision.Reason() != contract.PromotionReasonNone {
		t.Fatalf("approved pre-integration first tests were not ready: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
	}
	if _, ok := decision.Effect(); ok {
		t.Fatal("pre-integration readiness manufactured a canonical effect")
	}
	if _, present := context.Canonical.Suite().CurrentVersionID(); present {
		t.Fatal("readiness changed the supplied absent canonical state")
	}
}

func TestBootstrapFirstTestsReadinessRequiresRealAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*contract.PromotionContext)
		reason contract.PromotionReason
	}{
		{"no consent", func(c *contract.PromotionContext) { c.Consent = contract.Consent{} }, contract.PromotionReasonApprovalMissing},
		{"no assessment", func(c *contract.PromotionContext) { c.Assessment = contract.IntegrityAssessment{} }, contract.PromotionReasonIntegrityNotPassed},
		{"stale state", func(c *contract.PromotionContext) { c.ExpectedStateRevision++ }, contract.PromotionReasonStateChanged},
		{"stale schedule", func(c *contract.PromotionContext) { c.ExpectedSchedulingGeneration++ }, contract.PromotionReasonSchedulingBlocked},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
			context := lifecycleContext(t, lifecycleAbsent(t), manifest, "first-tests", "first-pr", "candidate", "candidate")
			test.change(&context)
			decision, err := contract.DecideBootstrap(contract.BootstrapInput{Mode: contract.FirstTestBootstrap, Promotion: contract.PromotionInput{Context: context}})
			if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != test.reason {
				t.Fatalf("missing prerequisite did not block readiness: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
			}
			if _, ok := decision.Effect(); ok {
				t.Fatal("blocked bootstrap returned a canonical effect")
			}
		})
	}
}
