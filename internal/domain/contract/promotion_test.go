package contract_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func promotionManifest(t *testing.T, contents ...string) artifact.Manifest {
	t.Helper()
	entries := make([]artifact.Entry, len(contents))
	for i, content := range contents {
		entries[i] = artifact.Entry{Path: string(rune('a'+i)) + "_test.go", Content: artifact.Hash([]byte(content))}
	}
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func promotionProtected(t *testing.T, manifest artifact.Manifest, scope string, inputs map[string]string) contract.ProtectedContract {
	t.Helper()
	protected, err := contract.NewProtectedContract(manifest, artifact.Hash([]byte(scope)), inputs)
	if err != nil {
		t.Fatal(err)
	}
	return protected
}

func TestProtectedContractPreservesExactImmutableInputs(t *testing.T) {
	manifest := promotionManifest(t, "test")
	inputs := map[string]string{" runner ": " version-1 "}
	protected := promotionProtected(t, manifest, "scope", inputs)
	if protected.IsZero() || protected.Manifest().Digest() != manifest.Digest() || protected.ScopeDigest() != artifact.Hash([]byte("scope")) || !reflect.DeepEqual(protected.CoveredInputs(), inputs) {
		t.Fatal("constructed protected contract lost exact manifest, scope or context")
	}
	inputs[" runner "] = "changed"
	returned := protected.CoveredInputs()
	returned[" runner "] = "changed again"
	if protected.CoveredInputs()[" runner "] != " version-1 " {
		t.Fatal("caller map mutated protected context")
	}
	entries := protected.Manifest().Entries()
	entries[0].Path = "changed"
	if protected.Manifest().Entries()[0].Path != "a_test.go" {
		t.Fatal("returned manifest mutated protected inventory")
	}
	equal := promotionProtected(t, manifest, "scope", map[string]string{" runner ": " version-1 "})
	if !protected.Equal(equal) || !equal.Equal(protected) {
		t.Fatal("equal exact protected contracts differ")
	}
	for name, other := range map[string]contract.ProtectedContract{
		"absent":          {},
		"manifest":        promotionProtected(t, promotionManifest(t, "changed"), "scope", map[string]string{" runner ": " version-1 "}),
		"scope":           promotionProtected(t, manifest, "other", map[string]string{" runner ": " version-1 "}),
		"context":         promotionProtected(t, manifest, "scope", map[string]string{" runner ": "version-1"}),
		"context removal": promotionProtected(t, manifest, "scope", nil),
	} {
		if protected.Equal(other) || other.Equal(protected) {
			t.Errorf("%s compared equal", name)
		}
	}
	if (contract.ProtectedContract{}).Equal(contract.ProtectedContract{}) {
		t.Fatal("absent contracts compared equal")
	}
	empty := promotionProtected(t, promotionManifest(t), "scope", nil)
	emptyMap := promotionProtected(t, promotionManifest(t), "scope", map[string]string{})
	if empty.IsZero() || !empty.Equal(emptyMap) {
		t.Fatal("constructed empty inventory or nil context equality lost")
	}
}

func TestProtectedContractRejectsMissingInputs(t *testing.T) {
	manifest := promotionManifest(t, "test")
	scope := artifact.Hash([]byte("scope"))
	for _, test := range []struct {
		name     string
		manifest artifact.Manifest
		scope    artifact.Digest
		inputs   map[string]string
	}{
		{"manifest", artifact.Manifest{}, scope, nil},
		{"scope", manifest, artifact.Digest{}, nil},
		{"empty key", manifest, scope, map[string]string{"": "v"}},
		{"blank key", manifest, scope, map[string]string{" \t": "v"}},
		{"empty value", manifest, scope, map[string]string{"key": ""}},
		{"blank value", manifest, scope, map[string]string{"key": "\n "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.NewProtectedContract(test.manifest, test.scope, test.inputs)
			if !errors.Is(err, contract.ErrInvalidProtectedContract) || !got.IsZero() {
				t.Fatalf("invalid protected input accepted: value=%v err=%v", got, err)
			}
		})
	}
}

func promotionSuite(t *testing.T, project contract.ProjectID, suite contract.SuiteID, version contract.SuiteVersionID, revision contract.StateRevision) contract.Suite {
	t.Helper()
	value, err := contract.NewSuite(project, suite, version, revision)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionVersion(t *testing.T, project contract.ProjectID, suite contract.SuiteID, version contract.SuiteVersionID, manifest artifact.Manifest) contract.SuiteVersion {
	t.Helper()
	value, err := contract.NewSuiteVersion(project, suite, version, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionCanonicalParts(t *testing.T) (contract.Suite, contract.SuiteVersion, contract.ProtectedContract, contract.PromotionRecord) {
	t.Helper()
	protected := promotionProtected(t, promotionManifest(t, "old"), "scope", map[string]string{"runner": "v1"})
	return promotionSuite(t, "project", "suite", "v1", 10), promotionVersion(t, "project", "suite", "v1", protected.Manifest()), protected, promotionRecord(t, promotionRecordInput(t))
}

func promotionCanonical(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	suite, version, protected, record := promotionCanonicalParts(t)
	value, err := contract.NewCanonicalSnapshot(suite, version, protected, record)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionAbsentCanonical(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	value, err := contract.NewCanonicalSnapshot(promotionSuite(t, "project", "suite", "", 10), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCanonicalSnapshotPreservesCompleteCurrentContract(t *testing.T) {
	current := promotionCanonical(t)
	suite, version, protected, record := promotionCanonicalParts(t)
	if current.IsZero() || current.Suite() != suite || current.Version().ID() != version.ID() || !current.Contract().Equal(protected) || current.Record().OperationID() != record.OperationID() || !current.Record().Binding().Equal(record.Binding()) {
		t.Fatal("canonical snapshot lost pointer, version, protected context or promotion provenance")
	}
	absent := promotionAbsentCanonical(t)
	if absent.IsZero() || absent.Suite().ID() != "suite" || absent.Version().ID() != "" || !absent.Contract().IsZero() || !absent.Record().IsZero() {
		t.Fatal("valid absent canonical was lost")
	}
	if !(contract.CanonicalSnapshot{}).IsZero() {
		t.Fatal("zero snapshot became a valid absent canonical")
	}
	context := current.Contract().CoveredInputs()
	context["runner"] = "changed"
	if current.Contract().CoveredInputs()["runner"] != "v1" {
		t.Fatal("snapshot protected context mutated")
	}
}

func TestCanonicalSnapshotRejectsPartialOrContradictoryFacts(t *testing.T) {
	suite, version, protected, record := promotionCanonicalParts(t)
	absent := promotionSuite(t, "project", "suite", "", 10)
	for _, test := range []struct {
		name      string
		suite     contract.Suite
		version   contract.SuiteVersion
		protected contract.ProtectedContract
		record    contract.PromotionRecord
	}{
		{"zero suite", contract.Suite{}, version, protected, record},
		{"zero all", contract.Suite{}, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}},
		{"pointer without version", suite, contract.SuiteVersion{}, protected, record},
		{"pointer without protected", suite, version, contract.ProtectedContract{}, record},
		{"pointer without record", suite, version, protected, contract.PromotionRecord{}},
		{"absent pointer with version", absent, version, contract.ProtectedContract{}, contract.PromotionRecord{}},
		{"absent pointer with protected", absent, contract.SuiteVersion{}, protected, contract.PromotionRecord{}},
		{"absent pointer with record", absent, contract.SuiteVersion{}, contract.ProtectedContract{}, record},
		{"version project", suite, promotionVersion(t, "other", "suite", "v1", protected.Manifest()), protected, record},
		{"version suite", suite, promotionVersion(t, "project", "other", "v1", protected.Manifest()), protected, record},
		{"version identity", suite, promotionVersion(t, "project", "suite", "other", protected.Manifest()), protected, record},
		{"version content", suite, promotionVersion(t, "project", "suite", "v1", promotionManifest(t, "changed")), protected, record},
		{"protected scope", suite, version, promotionProtected(t, protected.Manifest(), "changed", protected.CoveredInputs()), record},
		{"protected context", suite, version, promotionProtected(t, protected.Manifest(), "scope", nil), record},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.NewCanonicalSnapshot(test.suite, test.version, test.protected, test.record)
			if !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) || !got.IsZero() {
				t.Fatalf("contradictory canonical facts accepted: err=%v", err)
			}
		})
	}
	for name, change := range map[string]func(*contract.PromotionRecordInput, *contract.BindingInput){
		"record version": func(r *contract.PromotionRecordInput, b *contract.BindingInput) { r.VersionID = "other" },
		"record project": func(r *contract.PromotionRecordInput, b *contract.BindingInput) { b.Reference.ProjectID = "other" },
		"record suite":   func(r *contract.PromotionRecordInput, b *contract.BindingInput) { b.Reference.SuiteID = "other" },
		"record manifest": func(r *contract.PromotionRecordInput, b *contract.BindingInput) {
			b.Manifest = promotionManifest(t, "different").Digest()
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := promotionRecordInput(t)
			b := input.Binding
			binding := contract.BindingInput{Reference: b.Reference(), ExpectedCanonical: b.ExpectedCanonical(), Manifest: b.ManifestDigest(), Scope: b.ScopeDigest(), PolicyRevision: b.PolicyRevisionID(), CoveredInputs: b.CoveredInputs()}
			change(&input, &binding)
			var err error
			input.Binding, err = contract.NewApprovalBinding(binding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := contract.NewCanonicalSnapshot(suite, version, protected, promotionRecord(t, input))
			if !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) || !got.IsZero() {
				t.Fatalf("inconsistent record accepted: %v", err)
			}
		})
	}
}

func TestCanonicalContractClassificationUsesAllProtectedInputs(t *testing.T) {
	current := promotionCanonical(t)
	protected := promotionProtected(t, promotionManifest(t, "old"), "scope", map[string]string{"runner": "v1"})
	for _, test := range []struct {
		name     string
		current  contract.CanonicalSnapshot
		proposed contract.ProtectedContract
		want     contract.ContractChange
	}{
		{"same contract", current, protected, contract.ContractUnchanged},
		{"manifest changed", current, promotionProtected(t, promotionManifest(t, "changed"), "scope", map[string]string{"runner": "v1"}), contract.ContractChanged},
		{"scope changed", current, promotionProtected(t, protected.Manifest(), "changed", map[string]string{"runner": "v1"}), contract.ContractChanged},
		{"context changed", current, promotionProtected(t, protected.Manifest(), "scope", map[string]string{"runner": "v2"}), contract.ContractChanged},
		{"context removed", current, promotionProtected(t, protected.Manifest(), "scope", nil), contract.ContractChanged},
		{"no canonical", promotionAbsentCanonical(t), protected, contract.ContractChanged},
		{"empty initial inventory", promotionAbsentCanonical(t), promotionProtected(t, promotionManifest(t), "scope", nil), contract.ContractChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.ClassifyContractChange(test.current, test.proposed)
			if err != nil || got != test.want {
				t.Fatalf("change=%v err=%v, want %v", got, err, test.want)
			}
		})
	}
	if _, err := contract.ClassifyContractChange(contract.CanonicalSnapshot{}, protected); !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) {
		t.Fatalf("absent snapshot accepted: %v", err)
	}
	if _, err := contract.ClassifyContractChange(current, contract.ProtectedContract{}); !errors.Is(err, contract.ErrInvalidProtectedContract) {
		t.Fatalf("absent proposed contract accepted: %v", err)
	}
}

func promotionIntegration(t *testing.T, source contract.SourceRevision, carrier contract.ApprovalCarrierID, kind contract.IntegrationKind) contract.Integration {
	t.Helper()
	value,err:=contract.NewIntegration("project","main",source,carrier,kind)
	if err!=nil {t.Fatal(err)}
	return value
}

func TestIntegrationPreservesExactTrustedObservations(t *testing.T) {
	merged,err:=contract.NewIntegration(" project "," main "," source "," carrier ",contract.IntegrationMergedChange)
	if err!=nil {t.Fatal(err)}
	if merged.IsZero()||merged.ProjectID()!=" project "||merged.Target()!=" main "||merged.Source()!=" source "||merged.Carrier()!=" carrier "||merged.Kind()!=contract.IntegrationMergedChange {t.Fatal("merged observation lost exact supplied facts")}
	baseline:=promotionIntegration(t,"baseline","",contract.IntegrationExistingBaseline)
	if baseline.IsZero()||baseline.Carrier()!=""||baseline.Source()!="baseline"||baseline.Kind()!=contract.IntegrationExistingBaseline {t.Fatal("baseline observation invented an approval carrier")}
	if !(contract.Integration{}).IsZero() {t.Fatal("absent integration became confirmation")}
}

func TestIntegrationRejectsIncompleteObservations(t *testing.T) {
	for _,test:=range []struct{name string;project contract.ProjectID;target contract.IntegrationTargetID;source contract.SourceRevision;carrier contract.ApprovalCarrierID;kind contract.IntegrationKind}{
		{"project absent","","main","source","carrier",contract.IntegrationMergedChange},
		{"project blank"," \t","main","source","carrier",contract.IntegrationMergedChange},
		{"target absent","project","","source","carrier",contract.IntegrationMergedChange},
		{"target blank","project"," ","source","carrier",contract.IntegrationMergedChange},
		{"source absent","project","main","","carrier",contract.IntegrationMergedChange},
		{"source blank","project","main","\n","carrier",contract.IntegrationMergedChange},
		{"merged carrier absent","project","main","source","",contract.IntegrationMergedChange},
		{"merged carrier blank","project","main","source","\t",contract.IntegrationMergedChange},
		{"baseline carrier","project","main","source","carrier",contract.IntegrationExistingBaseline},
		{"baseline blank carrier","project","main","source"," ",contract.IntegrationExistingBaseline},
		{"kind absent","project","main","source","carrier",0},
		{"kind unknown","project","main","source","carrier",99},
	}{
		t.Run(test.name,func(t *testing.T){got,err:=contract.NewIntegration(test.project,test.target,test.source,test.carrier,test.kind);if !errors.Is(err,contract.ErrInvalidIntegration)||!got.IsZero(){t.Fatalf("invalid integration accepted: %v",err)}})
	}
}
