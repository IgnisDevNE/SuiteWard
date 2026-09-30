package contract_test

import (
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func lifecycleManifest(t *testing.T, entries ...artifact.Entry) artifact.Manifest {
	t.Helper()
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func lifecycleVersion(t *testing.T, project contract.ProjectID, suite contract.SuiteID, id contract.SuiteVersionID, manifest artifact.Manifest) contract.SuiteVersion {
	t.Helper()
	version, err := contract.NewSuiteVersion(project, suite, id, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func lifecycleRecordInput(t *testing.T, version contract.SuiteVersion, proposal contract.ProposalID, carrier contract.ApprovalCarrierID) contract.PromotionRecordInput {
	t.Helper()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{
		Reference: contract.ProposalReference{ProjectID: version.ProjectID(), SuiteID: version.SuiteID(), ProposalID: proposal, RevisionID: "revision-1"},
		Manifest:  version.Manifest().Digest(), Scope: artifact.Hash([]byte("scope")), PolicyRevision: "policy",
		CoveredInputs: map[string]string{"config": "v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return contract.PromotionRecordInput{
		OperationID: contract.OperationID("operation-" + string(version.ID())), VersionID: version.ID(), Binding: binding,
		Carrier: carrier, Source: "integrated-source", Target: "main", RecordedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	}
}

func lifecycleRecord(t *testing.T, input contract.PromotionRecordInput) contract.PromotionRecord {
	t.Helper()
	record, err := contract.NewPromotionRecord(input)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func lifecycleEstablished(t *testing.T, version contract.SuiteVersion, record contract.PromotionRecord) contract.CanonicalSnapshot {
	t.Helper()
	suite, err := contract.NewSuite(version.ProjectID(), version.SuiteID(), version.ID(), 9)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := contract.NewProtectedContract(version.Manifest(), record.Binding().ScopeDigest(), record.Binding().CoveredInputs())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := contract.NewCanonicalSnapshot(suite, version, protected, record)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func lifecycleHistory(t *testing.T, version contract.SuiteVersion, record contract.PromotionRecord) contract.HistoricalCanonical {
	t.Helper()
	history, err := contract.NewHistoricalCanonical(version, record)
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func TestHistoricalCanonicalRetainsExactVersionAndProvenance(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/a.go", Content: artifact.Hash([]byte("old test"))})
	version := lifecycleVersion(t, "project", "suite", "v1", manifest)
	record := lifecycleRecord(t, lifecycleRecordInput(t, version, "historical-proposal", "historical-carrier"))
	history, err := contract.NewHistoricalCanonical(version, record)
	if err != nil {
		t.Fatal(err)
	}
	if history.IsZero() || history.Version().ID() != version.ID() || history.Version().Manifest().Digest() != manifest.Digest() ||
		!history.Record().Binding().Equal(record.Binding()) || history.Record().Carrier() != record.Carrier() || history.Record().OperationID() != record.OperationID() {
		t.Fatal("historical canonical did not retain the exact supplied version and promotion provenance")
	}
	if !(contract.HistoricalCanonical{}).IsZero() {
		t.Fatal("unconstructed history claimed a canonical version")
	}
}

func TestHistoricalCanonicalRejectsInconsistentFacts(t *testing.T) {
	manifest := lifecycleManifest(t, artifact.Entry{Path: "tests/a.go", Content: artifact.Hash([]byte("old test"))})
	version := lifecycleVersion(t, "project", "suite", "v1", manifest)
	record := lifecycleRecord(t, lifecycleRecordInput(t, version, "historical-proposal", "historical-carrier"))
	otherManifest := lifecycleManifest(t, artifact.Entry{Path: "tests/a.go", Content: artifact.Hash([]byte("different test"))})
	cases := []struct {
		name    string
		version contract.SuiteVersion
		record  contract.PromotionRecord
	}{
		{"missing version", contract.SuiteVersion{}, record},
		{"missing record", version, contract.PromotionRecord{}},
		{"project mismatch", lifecycleVersion(t, "other-project", "suite", "v1", manifest), record},
		{"suite mismatch", lifecycleVersion(t, "project", "other-suite", "v1", manifest), record},
		{"version mismatch", lifecycleVersion(t, "project", "suite", "v2", manifest), record},
		{"manifest mismatch", lifecycleVersion(t, "project", "suite", "v1", otherManifest), record},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			history, err := contract.NewHistoricalCanonical(test.version, test.record)
			if !errors.Is(err, contract.ErrInvalidHistoricalCanonical) || !history.IsZero() {
				t.Fatalf("inconsistent history returned zero=%v error=%v", history.IsZero(), err)
			}
		})
	}
}

func lifecycleCorrection(t *testing.T, removeAddition bool) contract.CorrectionInput {
	t.Helper()
	oldTest := artifact.Entry{Path: "tests/a.go", Content: artifact.Hash([]byte("historical test"))}
	changedTest := artifact.Entry{Path: "tests/a.go", Content: artifact.Hash([]byte("changed test"))}
	addition := artifact.Entry{Path: "tests/b.go", Content: artifact.Hash([]byte("later addition"))}
	targetVersion := lifecycleVersion(t, "project", "suite", "v1", lifecycleManifest(t, oldTest))
	targetRecord := lifecycleRecord(t, lifecycleRecordInput(t, targetVersion, "target-proposal", "target-pr"))
	currentVersion := lifecycleVersion(t, "project", "suite", "v2", lifecycleManifest(t, changedTest, addition))
	currentInput := lifecycleRecordInput(t, currentVersion, "current-proposal", "current-pr")
	binding := currentInput.Binding
	var err error
	currentInput.Binding, err = contract.NewApprovalBinding(contract.BindingInput{
		Reference: binding.Reference(), ExpectedCanonical: targetVersion.ID(), Manifest: binding.ManifestDigest(),
		Scope: binding.ScopeDigest(), PolicyRevision: binding.PolicyRevisionID(), CoveredInputs: binding.CoveredInputs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := lifecycleEstablished(t, currentVersion, lifecycleRecord(t, currentInput))
	entries := []artifact.Entry{oldTest, addition}
	if removeAddition {
		entries = []artifact.Entry{oldTest}
	}
	context := lifecycleContext(t, canonical, lifecycleManifest(t, entries...), "correction-proposal", "correction-pr", "candidate", "integrated-correction")
	input := lifecycleInput(context)
	input.NewVersionID = "v3"
	input.Integration = lifecycleIntegration(t, "integrated-correction", "correction-pr", contract.IntegrationMergedChange)
	return contract.CorrectionInput{Promotion: input, Target: lifecycleHistory(t, targetVersion, targetRecord)}
}

func TestCorrectionCreatesFreshVersionAndRetainsHistoricalFacts(t *testing.T) {
	for _, removeAddition := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve later addition", true: "explicitly approve later deletion"}[removeAddition], func(t *testing.T) {
			input := lifecycleCorrection(t, removeAddition)
			current := input.Promotion.Context.Canonical
			target := input.Target
			decision, err := contract.DecideCorrection(input)
			if err != nil || decision.Outcome() != contract.PromotionProposed {
				t.Fatalf("exact approved correction was not proposed: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
			}
			effect, ok := decision.Effect()
			if !ok || effect.Version().ID() != "v3" || effect.ExpectedCanonicalID() != "v2" || effect.Promotion().CorrectsVersionID() != "v1" ||
				effect.Audit().Promotion().CorrectsVersionID() != "v1" || effect.Publication().Promotion().CorrectsVersionID() != "v1" {
				t.Fatal("correction lost fresh logical version, current baseline, or historical audit relation")
			}
			entries := effect.Version().Manifest().Entries()
			wantCount := 2
			if removeAddition {
				wantCount = 1
			}
			if len(entries) != wantCount || entries[0] != target.Version().Manifest().Entries()[0] || effect.Version().Manifest().Digest() != input.Promotion.Context.Proposed.Manifest().Digest() {
				t.Fatal("correction did not preserve the exact approved inventory and reused historical bytes")
			}
			if current.Version().ID() != "v2" || len(current.Version().Manifest().Entries()) != 2 || target.Version().ID() != "v1" || len(target.Version().Manifest().Entries()) != 1 ||
				input.Promotion.CorrectsVersionID != "" || current.Record().Binding().Reference().ProposalID != "current-proposal" || target.Record().Binding().Reference().ProposalID != "target-proposal" {
				t.Fatal("proposing correction rewrote supplied current or historical facts")
			}
		})
	}
}

func TestCorrectionDelegatesCurrentAuthorityAndIntegration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*contract.CorrectionInput)
		reason contract.PromotionReason
	}{
		{"historical consent", func(i *contract.CorrectionInput) {
			history := lifecycleContext(t, lifecycleAbsent(t), i.Target.Version().Manifest(), "target-proposal", "target-pr", "old-source", "old-source")
			i.Promotion.Context.Consent = history.Consent
		}, contract.PromotionReasonApprovalMissing},
		{"changed canonical baseline", func(i *contract.CorrectionInput) {
			i.Promotion.Context.Canonical = lifecycleEstablished(t, i.Target.Version(), i.Target.Record())
		}, contract.PromotionReasonCanonicalChanged},
		{"integration absent", func(i *contract.CorrectionInput) { i.Promotion.Integration = contract.Integration{} }, contract.PromotionReasonIntegrationMissing},
		{"stale scheduling", func(i *contract.CorrectionInput) { i.Promotion.Context.ExpectedSchedulingGeneration++ }, contract.PromotionReasonSchedulingBlocked},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := lifecycleCorrection(t, false)
			test.change(&input)
			decision, err := contract.DecideCorrection(input)
			if err != nil || decision.Outcome() != contract.PromotionBlocked || decision.Reason() != test.reason {
				t.Fatalf("correction bypassed shared guard: outcome=%v reason=%v error=%v", decision.Outcome(), decision.Reason(), err)
			}
			if _, ok := decision.Effect(); ok {
				t.Fatal("blocked correction created an effect")
			}
		})
	}
}

func TestCorrectionNoChangeDoesNotManufactureVersion(t *testing.T) {
	input := lifecycleCorrection(t, false)
	canonical := input.Promotion.Context.Canonical
	input.Promotion.Context = lifecycleContext(t, canonical, canonical.Version().Manifest(), "correction-proposal", "correction-pr", "candidate", "integrated-correction")
	input.Promotion.OperationID, input.Promotion.NewVersionID, input.Promotion.RecordedAt = "", "", time.Time{}
	decision, err := contract.DecideCorrection(input)
	if err != nil || decision.Outcome() != contract.PromotionNoChange {
		t.Fatalf("unchanged protected contract created a correction version: outcome=%v error=%v", decision.Outcome(), err)
	}
	if _, ok := decision.Effect(); ok {
		t.Fatal("no-change correction returned an effect")
	}
}

func TestCorrectionRevocationPreservesCompletedSnapshot(t *testing.T) {
	input := lifecycleCorrection(t, false)
	decision, err := contract.DecideCorrection(input)
	if err != nil {
		t.Fatal(err)
	}
	effect, ok := decision.Effect()
	if !ok {
		t.Fatal("valid correction has no promotion effect")
	}
	// Reconstitute the resulting state as supplied committed facts. This is a
	// domain snapshot test, not evidence of a durable or atomic commit.
	completed, err := contract.NewCanonicalSnapshot(effect.Suite(), effect.Version(), input.Promotion.Context.Proposed, effect.Promotion())
	if err != nil {
		t.Fatal(err)
	}
	context := input.Promotion.Context
	owner, err := contract.NewPrincipal("owner", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := contract.NewCommand(contract.CommandInput{OperationID: "revoke-operation", SourceCommandID: "revoke-source", Actor: owner,
		Reference: context.Reference, Carrier: context.Carrier, Action: contract.RevokeConsent, Order: 2})
	if err != nil {
		t.Fatal(err)
	}
	context.Consent, _, err = context.Consent.Apply(context.Proposal, context.Policy, revoke)
	if err != nil {
		t.Fatal(err)
	}
	if context.Consent.HasApproval(context.Proposal, context.Policy) {
		t.Fatal("revocation retained active consent")
	}
	input.Promotion.Context = context
	later, err := contract.DecideCorrection(input)
	if err != nil || later.Reason() != contract.PromotionReasonApprovalMissing {
		t.Fatalf("revoked consent still authorizes correction: %v %v", later.Reason(), err)
	}
	if completed.Version().ID() != "v3" || completed.Record().CorrectsVersionID() != "v1" || completed.Version().Manifest().Digest() != effect.Version().Manifest().Digest() ||
		input.Target.Version().ID() != "v1" || context.Canonical.Version().ID() != "v2" {
		t.Fatal("post-promotion revocation reset the pointer or rewrote history")
	}
}
