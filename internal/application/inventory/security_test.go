package inventory_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// classifyChange classifies the base tree after change against the base tree's own inventory.
func classifyChange(t *testing.T, change func(map[string]file)) (inventory.Classification, error) {
	t.Helper()
	base, err := inventory.Build(context.Background(), treeOf(t, baseFiles()))
	if err != nil {
		t.Fatalf("Build base: %v", err)
	}
	files := baseFiles()
	change(files)
	got, _, err := inventory.Classify(context.Background(), inventory.Governing{Scope: base.Scope(), Manifest: base.Manifest()}, treeOf(t, files))
	return got, err
}

// Moving a protected file out of the scope is a change of the protected inventory: the old path is Removed. The new
// path is not in the scope, so it is not listed anywhere.
func TestRenamingAProtectedFileToAnUnprotectedPathIsAProtectedChange(t *testing.T) {
	for name, from := range map[string]string{"a tests file": "tests/a_test.go", "the workflow": ".github/workflows/ci.yml", "the runner": "Makefile"} {
		t.Run(name, func(t *testing.T) {
			got, err := classifyChange(t, func(files map[string]file) {
				moved := files[from]
				delete(files, from)
				moved.path = "src/moved"
				files[moved.path] = moved
			})
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			want := inventory.Classification{Change: inventory.ProtectedChange, Removed: []string{from}, Added: []string{}, Modified: []string{}, ReducedPaths: []string{}, ReducedRules: []string{}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("classification = %+v, want %+v", got, want)
			}
		})
	}
}

// A link outside the scope that points at a protected file adds nothing to the inventory and changes no protected byte:
// the protected file is still read and hashed at its own path, so the candidate is Unchanged. The link is not rejected
// because no pattern covers it and none can reach below it.
func TestAnUnprotectedLinkToAProtectedFileChangesNothingProtected(t *testing.T) {
	for name, target := range map[string]string{"a tests file": "../tests/a_test.go", "the workflow": "../.github/workflows/ci.yml", "the declaration": "../.suiteward.yml"} {
		t.Run(name, func(t *testing.T) {
			got, err := classifyChange(t, func(files map[string]file) { files["docs/alias"] = link("docs/alias", target) })
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if want := (inventory.Classification{Change: inventory.Unchanged}); !reflect.DeepEqual(got, complete(want)) {
				t.Fatalf("classification = %+v, want %+v", got, complete(want))
			}
		})
	}
}

// The reverse swap fails closed: a link that takes the place of a protected file is itself covered, so the candidate is
// an unsafe entry, not a quiet removal of the file's content.
func TestALinkInPlaceOfAProtectedFileIsRejected(t *testing.T) {
	for _, path := range []string{"tests/a_test.go", ".github/workflows/ci.yml", "Makefile", ".suiteward.yml"} {
		t.Run(path, func(t *testing.T) {
			_, err := classifyChange(t, func(files map[string]file) { files[path] = link(path, "../src/main.go") })
			var unsafe *inventory.UnsafeEntryError
			if !errors.Is(err, inventory.ErrUnsafeEntry) || !errors.As(err, &unsafe) || unsafe.Path != path {
				t.Fatalf("Classify error = %v, want an UnsafeEntryError for %q", err, path)
			}
		})
	}
}

// Matching stays case-sensitive, but a path that case-folds equal to a covered path, or lies under a directory that a covered
// path lies in, is protected too: a case-insensitive checkout would put it on the protected file. A look-alike character is
// another character and makes another path.
func TestAPathThatCollidesInCaseWithAProtectedPathIsProtected(t *testing.T) {
	files := baseFiles()
	files["Tests/a_test.go"] = regular("Tests/a_test.go", "other")
	files["Tests/c_test.go"] = regular("Tests/c_test.go", "new")
	files["Tests/README.md"] = regular("Tests/README.md", "docs")
	files[".GitHub/README.md"] = regular(".GitHub/README.md", "docs")
	files["makefile"] = regular("makefile", "x")
	files[".GitHub/workflows/CI.yml"] = regular(".GitHub/workflows/CI.yml", "x")
	files["t\u0435sts/a_test.go"] = regular("t\u0435sts/a_test.go", "cyrillic") // Cyrillic e
	files["\uff54ests/a_test.go"] = regular("\uff54ests/a_test.go", "fullwidth")
	files["SRC/main.go"] = regular("SRC/main.go", "unprotected")
	tree := treeOf(t, files)

	built, err := inventory.Build(context.Background(), tree)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	protected := map[string]bool{}
	for _, entry := range built.Manifest().Entries() {
		protected[entry.Path] = true
	}
	for path, want := range map[string]bool{
		"tests/a_test.go": true, "Tests/a_test.go": true, "Tests/c_test.go": true, "Tests/README.md": true, ".GitHub/README.md": false, "makefile": true, ".GitHub/workflows/CI.yml": true,
		"t\u0435sts/a_test.go": false, "\uff54ests/a_test.go": false, "SRC/main.go": false, "src/main.go": false,
	} {
		if protected[path] != want {
			t.Errorf("%q protected = %v, want %v", path, protected[path], want)
		}
	}
}

// A new Tests/ file beside the protected tests/ is a change of the protected files, listed as added so that the approval binds
// its bytes.
func TestAddingAPathThatCollidesInCaseIsAProtectedChange(t *testing.T) {
	for name, path := range map[string]string{"the twin of a file": "Tests/a_test.go", "a new file in the twin directory": "Tests/b2_test.go", "a new file": "TESTS/new_test.go"} {
		t.Run(name, func(t *testing.T) {
			got, err := classifyChange(t, func(files map[string]file) { files[path] = regular(path, "other") })
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if want := complete(inventory.Classification{Change: inventory.ProtectedChange, Added: []string{path}}); !reflect.DeepEqual(got, want) {
				t.Fatalf("classification = %+v, want %+v", got, want)
			}
		})
	}
}

// Removing a collision is a change of the protected files and no reduction of the scope.
func TestRemovingACaseCollisionIsAProtectedChange(t *testing.T) {
	base := baseFiles()
	base["Tests/a_test.go"] = regular("Tests/a_test.go", "other")
	governing, err := inventory.Build(context.Background(), treeOf(t, base))
	if err != nil {
		t.Fatalf("Build base: %v", err)
	}
	got, _, err := inventory.Classify(context.Background(), inventory.Governing{Scope: governing.Scope(), Manifest: governing.Manifest()}, treeOf(t, baseFiles()))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if want := complete(inventory.Classification{Change: inventory.ProtectedChange, Removed: []string{"Tests/a_test.go"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("classification = %+v, want %+v", got, want)
	}
}

// A look-alike path, and a path that no covered path collides with, are Unchanged as before.
func TestAddingALookAlikePathIsUnchanged(t *testing.T) {
	for name, path := range map[string]string{"a Cyrillic e": "t\u0435sts/a_test.go", "a fullwidth t": "\uff54ests/a_test.go", "a combining mark": "te\u0301sts/a_test.go", "an unrelated file": "docs/Tests.md"} {
		t.Run(name, func(t *testing.T) {
			got, err := classifyChange(t, func(files map[string]file) { files[path] = regular(path, "other") })
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if want := complete(inventory.Classification{Change: inventory.Unchanged}); !reflect.DeepEqual(got, want) {
				t.Fatalf("classification = %+v, want %+v", got, want)
			}
		})
	}
}

// A link at a path that collides in case with a protected path is as unsafe as a link at the protected path: on a case-insensitive
// checkout it would stand where the protected file or directory is. A link at a look-alike path is accepted as any other.
func TestALinkThatCollidesInCaseWithAProtectedPathIsRejected(t *testing.T) {
	for name, path := range map[string]string{"the twin of a file": "Tests/a_test.go", "a file in the twin directory": "Tests/new_link", "the twin directory": "Tests", "the twin of the runner": "makefile"} {
		t.Run(name, func(t *testing.T) {
			_, err := classifyChange(t, func(files map[string]file) { files[path] = link(path, "../src/main.go") })
			var unsafe *inventory.UnsafeEntryError
			if !errors.Is(err, inventory.ErrUnsafeEntry) || !errors.As(err, &unsafe) || unsafe.Path != path {
				t.Fatalf("Classify error = %v, want an UnsafeEntryError for %q", err, path)
			}
		})
	}
	for name, path := range map[string]string{"a Cyrillic e": "t\u0435sts/a_test.go", "an unrelated name": "docs/Tests", "a twin directory that nothing protects": ".GitHub/link"} {
		t.Run(name, func(t *testing.T) {
			if _, err := classifyChange(t, func(files map[string]file) { files[path] = link(path, "../src/main.go") }); err != nil {
				t.Fatalf("Classify: %v", err)
			}
		})
	}
}

// A path in a case-twin directory is protected when its path in the real-case directory would be covered, not for the directory
// alone: a pattern that covers only some files of tests/ leaves the other files of Tests/ out.
func TestACaseTwinDirectoryIsProtectedOnlyWhereTheRealDirectoryWouldBe(t *testing.T) {
	files := map[string]file{}
	for _, f := range []file{
		regular(declaration, "version: 1\ninclude: [\"**/*_test.go\"]\n"),
		regular("tests/a_test.go", "a"), regular("tests/README.md", "r"),
		regular("Tests/b_test.go", "b"), regular("Tests/README.md", "r"),
		regular("TESTS/Sub/README.md", "r"), regular("tests/Sub/c_test.go", "c"), regular("TESTS/Sub/d_test.go", "d"),
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
	for path, want := range map[string]bool{
		"tests/a_test.go": true, "Tests/b_test.go": true, "TESTS/Sub/d_test.go": true,
		"tests/README.md": false, "Tests/README.md": false, "TESTS/Sub/README.md": false,
	} {
		if protected[path] != want {
			t.Errorf("%q protected = %v, want %v", path, protected[path], want)
		}
	}
}

// When several directories fold together, the path is protected if any rewrite onto one of them is covered.
func TestAPathUnderDirectoriesThatFoldTogetherIsProtectedWhenAnyRewriteIsCovered(t *testing.T) {
	got, err := classifyChange(t, func(files map[string]file) {
		files["Tests/seed"] = regular("Tests/seed", "x") // a second real-case directory beside tests/
		files["TESTS/new.go"] = regular("TESTS/new.go", "y")
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := complete(inventory.Classification{Change: inventory.ProtectedChange, Added: []string{"TESTS/new.go", "Tests/seed"}})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classification = %+v, want %+v", got, want)
	}
}
