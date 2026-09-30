package artifact_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestManifestGoldenIdentity(t *testing.T) {
	first := artifact.Entry{Path: "tests/a_test.go", Content: artifact.Hash([]byte("abc"))}
	second := artifact.Entry{Path: "tests/b_test.go", Content: artifact.Hash(nil)}
	tests := []struct {
		name    string
		entries []artifact.Entry
		want    string
	}{
		{"nil", nil, "sha256:ff3f0b17ac399c212f3042c9245011cac8025a8ee5600ac4191df021339b8b15"},
		{"empty", []artifact.Entry{}, "sha256:ff3f0b17ac399c212f3042c9245011cac8025a8ee5600ac4191df021339b8b15"},
		{"one", []artifact.Entry{first}, "sha256:706968403fd8612b8bb22f79e659287097e0165687d01bdf1b54bc0f79557fb6"},
		{"two", []artifact.Entry{first, second}, "sha256:6840a6b34ebc7a3e9bd2b9b1b9437f9b23af4b8be0dffea6b284c9ef839506d4"},
		{"reversed", []artifact.Entry{second, first}, "sha256:6840a6b34ebc7a3e9bd2b9b1b9437f9b23af4b8be0dffea6b284c9ef839506d4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := newManifest(t, tt.entries)
			if manifest.Digest().String() != tt.want {
				t.Errorf("manifest digest = %q, want %q", manifest.Digest().String(), tt.want)
			}
			if manifest.IsZero() {
				t.Error("constructed manifests must have an identity, including empty inventories")
			}
		})
	}
}

func TestManifestIdentityCoversProtectedInventory(t *testing.T) {
	content := artifact.Hash([]byte("test content"))
	original := newManifest(t, []artifact.Entry{{Path: "tests/caf\u00e9.go", Content: content}})
	tests := []struct {
		name    string
		entries []artifact.Entry
	}{
		{"path change", []artifact.Entry{{Path: "tests/other.go", Content: content}}},
		{"case change", []artifact.Entry{{Path: "Tests/caf\u00e9.go", Content: content}}},
		{"unicode normalization", []artifact.Entry{{Path: "tests/cafe\u0301.go", Content: content}}},
		{"content change", []artifact.Entry{{Path: "tests/caf\u00e9.go", Content: artifact.Hash([]byte("changed"))}}},
		{"addition", []artifact.Entry{{Path: "tests/caf\u00e9.go", Content: content}, {Path: "tests/new.go", Content: content}}},
		{"removal", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := newManifest(t, tt.entries)
			if original.Digest() == changed.Digest() {
				t.Fatal("changing the exact inventory must change its identity")
			}
		})
	}
}

func TestManifestPreservesInputAndOutput(t *testing.T) {
	content := artifact.Hash([]byte("test content"))
	input := []artifact.Entry{
		{Path: "tests/z.go", Content: content},
		{Path: "tests/a.go", Content: content},
	}
	before := slices.Clone(input)
	manifest := newManifest(t, input)
	if !slices.Equal(input, before) {
		t.Fatal("constructing a manifest changed the caller's entry order")
	}
	want := []artifact.Entry{before[1], before[0]}
	if !slices.Equal(manifest.Entries(), want) {
		t.Fatal("entries must be returned in canonical path order")
	}
	digest := manifest.Digest()
	input[0] = artifact.Entry{Path: "replaced.go", Content: artifact.Hash([]byte("replacement"))}
	returned := manifest.Entries()
	returned[0] = artifact.Entry{Path: "also-replaced.go", Content: artifact.Hash(nil)}
	if !slices.Equal(manifest.Entries(), want) || manifest.Digest() != digest {
		t.Fatal("retained inputs or returned entries modified an existing manifest")
	}
}

func TestManifestAbsentIsDifferentFromEmptyInventory(t *testing.T) {
	var absent artifact.Manifest
	if !absent.IsZero() || !absent.Digest().IsZero() || len(absent.Entries()) != 0 {
		t.Fatal("unconstructed manifest must have neither inventory nor identity")
	}
	empty := newManifest(t, nil)
	if empty.IsZero() || empty.Digest() == absent.Digest() {
		t.Fatal("a constructed empty inventory must differ from an absent manifest")
	}
}

func TestManifestRejectsInvalidPathsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"absolute", "/tests/a.go"},
		{"trailing slash", "tests/"},
		{"empty component", "tests//a.go"},
		{"dot", "."},
		{"dot prefix", "./tests/a.go"},
		{"dot middle", "tests/./a.go"},
		{"dot suffix", "tests/."},
		{"parent", ".."},
		{"parent prefix", "../tests/a.go"},
		{"parent middle", "tests/../a.go"},
		{"parent suffix", "tests/.."},
		{"backslash", `tests\a.go`},
		{"drive absolute", "C:/tests/a.go"},
		{"drive relative", "C:tests/a.go"},
		{"colon", "tests/a:b.go"},
		{"nul", "tests/a\x00.go"},
		{"invalid utf8", "tests/\xff.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := artifact.Entry{Path: tt.path, Content: artifact.Hash([]byte("test"))}
			manifest, err := artifact.NewManifest([]artifact.Entry{entry})
			if !errors.Is(err, artifact.ErrInvalidPath) {
				t.Fatalf("NewManifest(%q) error = %v, want ErrInvalidPath", tt.path, err)
			}
			if !manifest.IsZero() {
				t.Fatal("rejected inventory must not return a constructed manifest")
			}
		})
	}
}

func TestManifestRejectsDuplicatePaths(t *testing.T) {
	for _, content := range []string{"same", "different"} {
		t.Run(content, func(t *testing.T) {
			entries := []artifact.Entry{
				{Path: "tests/a.go", Content: artifact.Hash([]byte("same"))},
				{Path: "tests/b.go", Content: artifact.Hash(nil)},
				{Path: "tests/a.go", Content: artifact.Hash([]byte(content))},
			}
			manifest, err := artifact.NewManifest(entries)
			if !errors.Is(err, artifact.ErrDuplicatePath) {
				t.Fatalf("duplicate inventory error = %v, want ErrDuplicatePath", err)
			}
			if !manifest.IsZero() {
				t.Fatal("duplicate inventory must not return a constructed manifest")
			}
		})
	}
}

func TestManifestRejectsAbsentContentIdentity(t *testing.T) {
	manifest, err := artifact.NewManifest([]artifact.Entry{{Path: "tests/a.go"}})
	if !errors.Is(err, artifact.ErrInvalidDigest) {
		t.Fatalf("absent content identity error = %v, want ErrInvalidDigest", err)
	}
	if !manifest.IsZero() {
		t.Fatal("absent content identity must not return a constructed manifest")
	}
}

func TestManifestAcceptsDistinctExactPaths(t *testing.T) {
	paths := []string{"tests/A.go", "tests/a.go", "tests/caf\u00e9.go", "tests/cafe\u0301.go", ".tests/a..go", "tests/CON"}
	entries := make([]artifact.Entry, len(paths))
	for i, path := range paths {
		entries[i] = artifact.Entry{Path: path, Content: artifact.Hash(nil)}
	}
	manifest := newManifest(t, entries)
	if len(manifest.Entries()) != len(paths) {
		t.Fatal("distinct exact paths were collapsed")
	}
	got := make([]string, 0, len(paths))
	for _, entry := range manifest.Entries() {
		got = append(got, entry.Path)
	}
	want := slices.Clone(paths)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatal("manifest paths were normalized or not sorted by their exact bytes")
	}
}

func newManifest(t *testing.T, entries []artifact.Entry) artifact.Manifest {
	t.Helper()
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatalf("NewManifest(%v): %v", entries, err)
	}
	return manifest
}
