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
