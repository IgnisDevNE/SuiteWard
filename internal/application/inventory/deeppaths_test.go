package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// deepPath is a path of exactly size bytes whose directories are one letter deep: x<n>/a/a/.../f.
func deepPath(n, size int) string {
	head := fmt.Sprintf("x%d/", n)
	rest := size - len(head) - 1
	return head + strings.Repeat("a/", rest/2) + strings.Repeat("b", rest%2) + "f"
}

// A path of a tree is at most 4096 bytes (D-SCOPE, tree limits): a longer one is an invalid tree, which keeps the governing scope in force.
func TestAPathLongerThan4096BytesIsInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		size int
		want bool
	}{"4096 bytes": {4096, true}, "4097 bytes": {4097, false}} {
		t.Run(name, func(t *testing.T) {
			files := baseFiles()
			path := deepPath(1, tc.size)
			if len(path) != tc.size {
				t.Fatalf("test path has %d bytes, want %d", len(path), tc.size)
			}
			files[path] = regular(path, "x")
			_, err := inventory.Build(context.Background(), treeOf(t, files))
			if tc.want && err != nil || !tc.want && !errors.Is(err, inventory.ErrInvalidTree) {
				t.Fatalf("Build error = %v, want valid: %v", err, tc.want)
			}
		})
	}
}

// Deep paths cost work in proportion to their length, not its square: a few thousand of them, 4 MB of paths in all, are
// resolved in about a second, where folding every directory prefix took tens of seconds.
func TestADeepTreeIsResolvedFast(t *testing.T) {
	files := baseFiles()
	for i := range 2000 {
		path := deepPath(i, 2000)
		files[path] = regular(path, "x")
	}
	tree := treeOf(t, files)
	start := time.Now()
	built, err := inventory.Build(context.Background(), tree)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, _, err := inventory.Classify(context.Background(), inventory.Governing{Scope: built.Scope(), Manifest: built.Manifest()}, tree); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Build and Classify of %d deep entries took %v", len(files), elapsed)
	}
}

// A character whose fold has another length (the Kelvin sign is 3 bytes and folds to k) keeps the twin rewrite on the right prefix.
func TestACaseTwinDirectoryWithAnotherByteLengthIsProtectedWhereTheRealDirectoryWouldBe(t *testing.T) {
	files := map[string]file{}
	for _, f := range []file{
		regular(declaration, "version: 1\ninclude: [/keys/sub/**]\n"),
		regular("keys/sub/a.go", "a"), regular("keys/other/b.go", "b"),
		regular("\u212aeys/sub/c.go", "c"), regular("\u212aeys/other/d.go", "d"),
	} {
		files[f.path] = f
	}
	built, err := inventory.Build(context.Background(), treeOf(t, files))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	protected := map[string]bool{}
	for _, entry := range built.Manifest().Entries() {
		protected[entry.Path] = true
	}
	for path, want := range map[string]bool{"keys/sub/a.go": true, "\u212aeys/sub/c.go": true, "keys/other/b.go": false, "\u212aeys/other/d.go": false} {
		if protected[path] != want {
			t.Errorf("%q protected = %v, want %v", path, protected[path], want)
		}
	}
}

// The governing evaluation of a candidate needs which paths the governing scope covers, not their content: Classify reads each covered file once, for the
// candidate's own manifest.
func TestClassifyReadsEachFileOnce(t *testing.T) {
	tree := treeOf(t, baseFiles())
	built, err := inventory.Build(context.Background(), tree)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tree.reads = nil

	if _, _, err := inventory.Classify(context.Background(), inventory.Governing{Scope: built.Scope(), Manifest: built.Manifest()}, tree); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	seen := map[string]bool{}
	for _, path := range tree.reads {
		if seen[path] {
			t.Errorf("%q was read more than once", path)
		}
		seen[path] = true
	}
}
