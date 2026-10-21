package artifact_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// Paths are byte-exact: a manifest holding Tests/a_test.go and tests/a_test.go keeps both entries, in byte order, as
// two files of two directories, the way a git tree holds them. A case-insensitive checkout would fold them into one.
func TestManifestKeepsPathsThatDifferOnlyInCaseAsDistinctEntries(t *testing.T) {
	content := artifact.Hash([]byte("c"))
	manifest := newManifest(t, []artifact.Entry{{Path: "tests/a_test.go", Content: content}, {Path: "Tests/a_test.go", Content: content}})

	entries := manifest.Entries()
	if len(entries) != 2 || entries[0].Path != "Tests/a_test.go" || entries[1].Path != "tests/a_test.go" {
		t.Fatalf("entries = %v, want Tests/a_test.go then tests/a_test.go", entries)
	}
	if only := newManifest(t, []artifact.Entry{{Path: "tests/a_test.go", Content: content}}); only.Digest() == manifest.Digest() {
		t.Fatal("the manifest without the Tests/ file has the same identity")
	}
}
