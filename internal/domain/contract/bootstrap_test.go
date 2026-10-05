package contract_test

import (
	"errors"
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

func lifecycleContext(t *testing.T, canonical contract.CanonicalSnapshot, manifest artifact.Manifest, proposalID contract.ProposalID, carrier contract.ApprovalCarrierID, origin, assessedSource contract.SourceRevision, revisionIDs ...contract.ProposalRevisionID) contract.PromotionContext {
	t.Helper()
	protected := lifecycleProtected(t, manifest)
	baseline, _ := canonical.Suite().CurrentVersionID()
	revisionID := contract.ProposalRevisionID("revision-1")
	if len(revisionIDs) > 0 {
		revisionID = revisionIDs[0]
	}
	binding, err := contract.NewApprovalBinding(contract.BindingInput{
		Reference:         contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: proposalID, RevisionID: revisionID},
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
	return contract.PromotionContext{
		Canonical: canonical, Proposed: protected, Proposal: proposal, Reference: binding.Reference(), Carrier: carrier,
		Policy: policy, Consent: consent, Assessment: assessment,
	}
}

func lifecycleInput(context contract.PromotionContext) contract.PromotionInput {
	return contract.PromotionInput{
		Context: context, Target: "main", OperationID: "promote-operation", NewVersionID: "new-version",
		RecordedAt: time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC),
	}
}

func lifecycleBaselineInput(t *testing.T, context contract.PromotionContext, source contract.SourceRevision) contract.PromotionInput {
	t.Helper()
	input := lifecycleInput(context)
	input.Integration = lifecycleIntegration(t, source, "", contract.IntegrationExistingBaseline)
	return input
}

func lifecycleIntegration(t *testing.T, source contract.SourceRevision, carrier contract.ApprovalCarrierID, kind contract.IntegrationKind) contract.Integration {
	t.Helper()
	integration, err := contract.NewIntegration("project", "main", source, carrier, kind)
	if err != nil {
		t.Fatal(err)
	}
	return integration
}

func TestBootstrapRejectsMalformedLifecycleInput(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	context := lifecycleContext(t, lifecycleAbsent(t), manifest, "first-tests", "first-pr", "candidate", "candidate")
	cases := []struct {
		name   string
		change func(*contract.BootstrapInput)
	}{
		{"absent canonical snapshot", func(i *contract.BootstrapInput) { i.Promotion.Context.Canonical = contract.CanonicalSnapshot{} }},
		{"absent proposal", func(i *contract.BootstrapInput) { i.Promotion.Context.Proposal = contract.Proposal{} }},
		{"absent proposed inventory", func(i *contract.BootstrapInput) { i.Promotion.Context.Proposed = contract.ProtectedContract{} }},
		{"correction attribution", func(i *contract.BootstrapInput) { i.Promotion.CorrectsVersionID = "old-version" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := contract.BootstrapInput{Promotion: contract.PromotionInput{Context: context}}
			test.change(&input)
			decision, err := contract.DecideBootstrap(input)
			if !errors.Is(err, contract.ErrInvalidBootstrap) {
				t.Fatalf("invalid bootstrap returned outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
			}
			if _, ok := decision.Effect(); ok {
				t.Fatal("invalid bootstrap created an effect")
			}
		})
	}
}

func TestBootstrapRequiresAbsentCanonicalEvenWithFreshApproval(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/existing.go", Content: artifact.Hash([]byte("existing test"))})
	version := lifecycleVersion(t, "project", "suite", "existing-version", manifest)
	record := lifecycleRecord(t, lifecycleRecordInput(t, version, "old-proposal", "old-pr"))
	canonical := lifecycleEstablished(t, version, record)
	proposed := lifecycleManifest(t, artifact.Entry{Path: "tests/new.go", Content: artifact.Hash([]byte("new test"))})
	context := lifecycleContext(t, canonical, proposed, "fresh-proposal", "fresh-pr", "candidate", "candidate")
	decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: lifecycleBaselineInput(t, context, "candidate")})
	if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != contract.PromotionReasonCanonicalPresent {
		t.Fatalf("bootstrap replaced established canonical: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
	}
	if _, ok := decision.Effect(); ok {
		t.Fatal("bootstrap proposed an effect over existing canonical")
	}
	// The same approved contract still expects the existing baseline if its
	// caller instead supplies an absent canonical snapshot.
	context.Canonical = lifecycleAbsent(t)
	decision, err = contract.DecideBootstrap(contract.BootstrapInput{Promotion: lifecycleBaselineInput(t, context, "candidate")})
	if err != nil || decision.Reason() != contract.PromotionReasonCanonicalChanged {
		t.Fatalf("expected-present approval became initial authority: reason=%v error=%v", decision.Reason(), err)
	}
}

func TestBootstrapEmptyInventoryCannotEstablishContract(t *testing.T) {
	context := lifecycleContext(t, lifecycleAbsent(t), lifecycleManifest(t), "empty-proposal", "empty-pr", "candidate", "candidate")
	decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: lifecycleBaselineInput(t, context, "candidate")})
	if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != contract.PromotionReasonEmptyInventory {
		t.Fatalf("empty inventory became a ready initial contract: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
	}
	if _, ok := decision.Effect(); ok {
		t.Fatal("empty inventory created canonical effect")
	}
}

func TestBootstrapExistingBaselineProposesFirstCanonical(t *testing.T) {
	const source, carrier = contract.SourceRevision("pinned-baseline"), contract.ApprovalCarrierID("open-hosting-pr")
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	context := lifecycleContext(t, lifecycleAbsent(t), manifest, "bootstrap-proposal", carrier, source, source)
	input := lifecycleInput(context)
	input.Integration = lifecycleIntegration(t, source, "", contract.IntegrationExistingBaseline)
	decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
	if err != nil || decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("valid integrated bootstrap did not propose first canonical: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
	}
	effect, ok := decision.Effect()
	if !ok || effect.ExpectedCanonicalID() != "" || effect.Version().ID() != input.NewVersionID || effect.Promotion().Source() != source || effect.Promotion().Carrier() != carrier {
		t.Fatal("bootstrap effect lost absence, exact integrated origin, or independent approval carrier")
	}
	if effect.Version().Manifest().Digest() != manifest.Digest() || effect.Promotion().CorrectsVersionID() != "" {
		t.Fatal("bootstrap produced unrelated inventory or correction provenance")
	}
	if _, present := context.Canonical.Suite().CurrentVersionID(); present {
		t.Fatal("promotion proposal mutated supplied canonical state")
	}
}

func TestBootstrapRejectsSubstitutedFacts(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	cases := []struct {
		name        string
		assessed    contract.SourceRevision
		integration contract.Integration
		reason      contract.PromotionReason
	}{
		{"integration missing", "pinned", contract.Integration{}, contract.PromotionReasonIntegrationMissing},
		{"hosting merge cannot substitute baseline observation", "pinned", lifecycleIntegration(t, "pinned", "approval-pr", contract.IntegrationMergedChange), contract.PromotionReasonIntegrationMismatch},
		{"moving tip cannot replace pin", "new-tip", lifecycleIntegration(t, "new-tip", "", contract.IntegrationExistingBaseline), contract.PromotionReasonIntegrationMismatch},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			context := lifecycleContext(t, lifecycleAbsent(t), manifest, "bootstrap-proposal", "approval-pr", "pinned", test.assessed)
			input := lifecycleInput(context)
			input.Integration = test.integration
			decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
			if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != test.reason {
				t.Fatalf("substituted bootstrap facts accepted: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
			}
			if _, ok := decision.Effect(); ok {
				t.Fatal("rejected bootstrap created an effect")
			}
		})
	}
}
