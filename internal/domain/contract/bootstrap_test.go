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

// lifecycleContext builds the proposal from the kind a fixture means: a baseline (no canonical) comes from a branch, a change from a pull.
func lifecycleContext(t *testing.T, canonical contract.CanonicalSnapshot, manifest artifact.Manifest, proposalID contract.ProposalID, carrier contract.ApprovalCarrierID, origin, assessedSource contract.SourceRevision, revisionIDs ...contract.ProposalRevisionID) contract.PromotionContext {
	t.Helper()
	kind := contract.OriginPull
	if _, present := canonical.Suite().CurrentVersionID(); !present {
		kind = contract.OriginBranch
	}
	return lifecycleContextOfKind(t, canonical, manifest, proposalID, carrier, kind, origin, assessedSource, revisionIDs...)
}

func lifecycleContextOfKind(t *testing.T, canonical contract.CanonicalSnapshot, manifest artifact.Manifest, proposalID contract.ProposalID, carrier contract.ApprovalCarrierID, kind contract.OriginKind, origin, assessedSource contract.SourceRevision, revisionIDs ...contract.ProposalRevisionID) contract.PromotionContext {
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
	revision, err := contract.NewProposalRevision(binding, origin, kind, carrier)
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
	input := lifecycleInput(context)
	input.Integration = lifecycleIntegration(t, "candidate", "fresh-pr", contract.IntegrationMergedChange)
	decision, err = contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
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

// A PR head can become the first canonical only from its merge commit (ADR 0010, 2026-10-15): the kind of the
// revision, not the commit, selects the integration that may establish the contract.
func TestBootstrapIntegrationFollowsTheOriginKind(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	const carrier = contract.ApprovalCarrierID("bootstrap-pr")
	cases := []struct {
		name        string
		kind        contract.OriginKind
		origin      contract.SourceRevision
		source      contract.SourceRevision
		integration contract.IntegrationKind
		with        contract.ApprovalCarrierID // the carrier the integration names
		reason      contract.PromotionReason   // PromotionReasonNone: proposed
	}{
		{"existing baseline of a branch revision", contract.OriginBranch, "pinned", "pinned", contract.IntegrationExistingBaseline, "", contract.PromotionReasonNone},
		{"existing baseline cannot establish a PR head", contract.OriginPull, "pr-head", "pr-head", contract.IntegrationExistingBaseline, "", contract.PromotionReasonIntegrationMismatch},
		{"merged change of a PR head", contract.OriginPull, "pr-head", "merge-commit", contract.IntegrationMergedChange, carrier, contract.PromotionReasonNone},
		{"merged change of a PR head fast-forwarded", contract.OriginPull, "pr-head", "pr-head", contract.IntegrationMergedChange, carrier, contract.PromotionReasonNone},
		{"merged change cannot establish a pinned branch commit", contract.OriginBranch, "pinned", "merge-commit", contract.IntegrationMergedChange, carrier, contract.PromotionReasonIntegrationMismatch},
		{"merged change of another carrier", contract.OriginPull, "pr-head", "merge-commit", contract.IntegrationMergedChange, "other-pr", contract.PromotionReasonIntegrationMismatch},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			context := lifecycleContextOfKind(t, lifecycleAbsent(t), manifest, "bootstrap-proposal", carrier, test.kind, test.origin, test.source)
			input := lifecycleInput(context)
			input.Integration = lifecycleIntegration(t, test.source, test.with, test.integration)
			decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
			if err != nil {
				t.Fatal(err)
			}
			_, effect := decision.Effect()
			if test.reason == contract.PromotionReasonNone {
				if decision.Outcome() != contract.PromotionProposed || !effect {
					t.Fatalf("outcome=%v reason=%v effect=%v; want the first canonical proposed", decision.Outcome(), decision.Reason(), effect)
				}
				return
			}
			if decision.Outcome() != contract.PromotionBlocked || decision.Reason() != test.reason || effect {
				t.Fatalf("outcome=%v reason=%v effect=%v; want blocked %v without an effect", decision.Outcome(), decision.Reason(), effect, test.reason)
			}
		})
	}
}

func TestBootstrapMergedChangeIsStillBlockedByAnEstablishedCanonical(t *testing.T) {
	version := lifecycleVersion(t, "project", "suite", "existing-version", lifecycleManifest(t, artifact.Entry{Path: "tests/existing.go", Content: artifact.Hash([]byte("existing test"))}))
	canonical := lifecycleEstablished(t, version, lifecycleRecord(t, lifecycleRecordInput(t, version, "old-proposal", "old-pr")))
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/new.go", Content: artifact.Hash([]byte("new test"))})
	context := lifecycleContextOfKind(t, canonical, manifest, "fresh-proposal", "fresh-pr", contract.OriginPull, "pr-head", "merge-commit")
	input := lifecycleInput(context)
	input.Integration = lifecycleIntegration(t, "merge-commit", "fresh-pr", contract.IntegrationMergedChange)
	decision, err := contract.DecideBootstrap(contract.BootstrapInput{Promotion: input})
	if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != contract.PromotionReasonCanonicalPresent {
		t.Fatalf("outcome=%v reason=%v error=%v; want blocked canonical-present", decision.Outcome(), decision.Reason(), err)
	}
}

// DecidePromotion is a public domain function, reachable without DecideBootstrap: it must not let a PR head become the first
// canonical through the existing-baseline integration either.
func TestPromotionOfAPullRevisionRejectsTheExistingBaselineIntegration(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/first.go", Content: artifact.Hash([]byte("first test"))})
	for _, test := range []struct {
		kind   contract.OriginKind
		reason contract.PromotionReason
	}{
		{contract.OriginPull, contract.PromotionReasonIntegrationMismatch},
		{contract.OriginBranch, contract.PromotionReasonNone},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			context := lifecycleContextOfKind(t, lifecycleAbsent(t), manifest, "first-proposal", "first-pr", test.kind, "head", "head")
			input := lifecycleInput(context)
			input.Integration = lifecycleIntegration(t, "head", "", contract.IntegrationExistingBaseline)
			decision, err := contract.DecidePromotion(input)
			_, effect := decision.Effect()
			if err != nil || decision.Reason() != test.reason || effect != (test.reason == contract.PromotionReasonNone) {
				t.Fatalf("outcome=%v reason=%v effect=%v error=%v; want reason %v", decision.Outcome(), decision.Reason(), effect, err, test.reason)
			}
		})
	}
}
