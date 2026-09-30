package contract_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestSuiteVersionPreservesSnapshot(t *testing.T) {
	manifest := mustManifest(t, []artifact.Entry{{Path: "tests/example_test.go", Content: artifact.Hash([]byte("test content"))}})
	version, err := contract.NewSuiteVersion(" project-1 ", " suite-1 ", " version-1 ", manifest)
	if err != nil {
		t.Fatalf("NewSuiteVersion() error = %v", err)
	}
	if version.ProjectID() != " project-1 " || version.SuiteID() != " suite-1 " || version.ID() != " version-1 " {
		t.Errorf("version identity = (%q, %q, %q), want unchanged supplied identifiers", version.ProjectID(), version.SuiteID(), version.ID())
	}
	if version.Manifest().IsZero() || version.Manifest().Digest() != manifest.Digest() {
		t.Error("version did not preserve its constructed manifest identity")
	}
}

func TestSuiteVersionProtectsManifest(t *testing.T) {
	want := artifact.Entry{Path: "tests/example_test.go", Content: artifact.Hash([]byte("original"))}
	entries := []artifact.Entry{want}
	manifest := mustManifest(t, entries)
	version, err := contract.NewSuiteVersion("project-1", "suite-1", "version-1", manifest)
	if err != nil {
		t.Fatalf("NewSuiteVersion() error = %v", err)
	}

	entries[0] = artifact.Entry{Path: "tests/replaced_test.go", Content: artifact.Hash([]byte("replacement"))}
	exposed := version.Manifest().Entries()
	if len(exposed) != 1 {
		t.Fatalf("version inventory has %d entries, want 1", len(exposed))
	}
	exposed[0] = entries[0]

	stored := version.Manifest().Entries()
	if len(stored) != 1 || stored[0] != want {
		t.Errorf("version inventory = %v, want unchanged original entry %v", stored, want)
	}
	if version.Manifest().Digest() != manifest.Digest() {
		t.Error("mutation of caller-owned entries changed the version manifest digest")
	}
}

func TestSuiteVersionPreservesLogicalIdentity(t *testing.T) {
	manifest := mustManifest(t, []artifact.Entry{{Path: "tests/example_test.go", Content: artifact.Hash([]byte("shared content"))}})
	first, err := contract.NewSuiteVersion("project-1", "suite-1", "version-1", manifest)
	if err != nil {
		t.Fatalf("first NewSuiteVersion() error = %v", err)
	}
	second, err := contract.NewSuiteVersion("project-1", "suite-1", "version-2", manifest)
	if err != nil {
		t.Fatalf("second NewSuiteVersion() error = %v", err)
	}
	if first.ID() == second.ID() {
		t.Errorf("distinct logical versions collapsed to the same ID %q", first.ID())
	}
	if first.Manifest().IsZero() || first.Manifest().Digest() != second.Manifest().Digest() {
		t.Error("distinct logical versions did not preserve their shared content identity")
	}
}

func TestSuiteVersionEmptyInventoryDiffersFromAbsentCanonical(t *testing.T) {
	version, err := contract.NewSuiteVersion("project-1", "suite-1", "version-empty", mustManifest(t, nil))
	if err != nil {
		t.Fatalf("NewSuiteVersion() with empty inventory error = %v", err)
	}
	if version.Manifest().IsZero() || len(version.Manifest().Entries()) != 0 {
		t.Fatal("constructed empty inventory was not preserved as a valid manifest")
	}
	awaiting, err := contract.NewSuite("project-1", "suite-1", "", 0)
	if err != nil {
		t.Fatalf("NewSuite() without canonical error = %v", err)
	}
	recorded, err := contract.NewSuite("project-1", "suite-1", version.ID(), 1)
	if err != nil {
		t.Fatalf("NewSuite() with canonical reference error = %v", err)
	}
	if _, present := awaiting.CurrentVersionID(); present {
		t.Error("awaiting suite unexpectedly has a canonical reference")
	}
	if current, present := recorded.CurrentVersionID(); !present || current != version.ID() {
		t.Error("supplied canonical reference was confused with the empty inventory")
	}
}

func mustManifest(t *testing.T, entries []artifact.Entry) artifact.Manifest {
	t.Helper()
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatalf("NewManifest() error = %v", err)
	}
	return manifest
}
