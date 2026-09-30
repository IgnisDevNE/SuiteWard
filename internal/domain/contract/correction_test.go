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
