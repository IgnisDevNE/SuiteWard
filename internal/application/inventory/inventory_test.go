package inventory_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

const declaration = ".suiteward.yml"

// requireManifest checks that manifest holds exactly the want paths of
// files, each bound to the digest of its content.
func requireManifest(t *testing.T, manifest artifact.Manifest, files []file, want []string) {
	t.Helper()
	contents := map[string]string{}
	for _, f := range files {
		contents[f.path] = f.content
	}
	expected := make([]artifact.Entry, 0, len(want))
	for _, path := range want {
		expected = append(expected, artifact.Entry{Path: path, Content: artifact.Hash([]byte(contents[path]))})
	}
	slices.SortFunc(expected, func(a, b artifact.Entry) int { return strings.Compare(a.Path, b.Path) })
	if got := manifest.Entries(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("manifest entries = %v, want %v", paths(got), want)
	}
}

func paths(entries []artifact.Entry) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Path)
	}
	return result
}

// guard marks every File of tree outside want as unreadable.
func guard(tree *fakeTree, want []string) {
	for _, f := range tree.files {
		if !slices.Contains(want, f.path) {
			tree.unreadable[f.path] = true
		}
	}
}

func TestBuild(t *testing.T) {
	workflow := regular(".github/workflows/ci.yml", "on: push\n")
	tests := []struct {
		name  string
		files []file
		want  []string
		err   error
	}{
		{
			name:  "no declaration covers only the workflows",
			files: []file{workflow, regular("src/main.go", "package main\n"), regular("tests/a_test.go", "a"), regular(".github/dependabot.yml", "d")},
			want:  []string{".github/workflows/ci.yml"},
		},
		{
			name:  "no declaration and no workflows is an empty inventory",
			files: []file{regular("src/main.go", "package main\n")},
			want:  []string{},
		},
		{
			name: "declaration covers itself, include and runner minus exclude",
			files: []file{
				regular(declaration, "version: 1\ninclude: [/tests/**]\nexclude: [/tests/data/**]\nrunner: [/Makefile]\n"),
				workflow, regular("tests/a_test.go", "a"), regular("tests/data/big.bin", "big"),
				regular("Makefile", "test:\n"), regular("src/main.go", "package main\n"),
			},
			want: []string{".github/workflows/ci.yml", declaration, "Makefile", "tests/a_test.go"},
		},
		{
			name:  "excluding the declaration keeps it covered",
			files: []file{regular(declaration, "version: 1\nexclude: [/.suiteward.yml, '*.yml']\n"), workflow},
			want:  []string{declaration},
		},
		{
			name:  "invalid declaration",
			files: []file{regular(declaration, "version: 2\n"), workflow},
			err:   scope.ErrInvalidDeclaration,
		},
		{
			name:  "empty declaration",
			files: []file{regular(declaration, "")},
			err:   scope.ErrInvalidDeclaration,
		},
		{
			name:  "declaration as a symlink",
			files: []file{symlink(declaration), workflow},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "declaration as a submodule",
			files: []file{submodule(declaration)},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "symlink inside an included directory",
			files: []file{regular(declaration, "version: 1\ninclude: [/tests/**]\n"), symlink("tests/link")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "symlink inside an excluded part of an included directory",
			files: []file{regular(declaration, "version: 1\ninclude: [/tests/**]\nexclude: [/tests/fixtures/**]\n"), symlink("tests/fixtures/link")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "symlink in place of a workflow",
			files: []file{symlink(".github/workflows/ci.yml")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "symlink in place of the workflows directory",
			files: []file{symlink(".github/workflows")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "submodule that is a parent of an anchored include",
			files: []file{regular(declaration, "version: 1\ninclude: [/vendor/tests/**]\n"), submodule("vendor")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "any submodule when an unanchored include exists",
			files: []file{regular(declaration, "version: 1\ninclude: ['*_test.go']\n"), submodule("third_party/lib")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "any symlink when an unanchored runner entry exists",
			files: []file{regular(declaration, "version: 1\nrunner: [Makefile]\n"), symlink("docs/link")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name: "symlink and submodule outside every pattern are ignored",
			files: []file{
				regular(declaration, "version: 1\ninclude: [/tests/**]\n"), regular("tests/a_test.go", "a"),
				symlink("docs/link"), submodule("third_party/lib"), symlink("testsuite"),
			},
			want: []string{declaration, "tests/a_test.go"},
		},
		{
			name:  "invalid path",
			files: []file{regular("a//b", "x")},
			err:   inventory.ErrInvalidTree,
		},
		{
			name:  "parent segment",
			files: []file{regular("../x", "x")},
			err:   inventory.ErrInvalidTree,
		},
		{
			name:  "absolute path",
			files: []file{regular("/etc/passwd", "x")},
			err:   inventory.ErrInvalidTree,
		},
		{
			name:  "kind zero",
			files: []file{{path: "src/main.go", kind: 0}},
			err:   inventory.ErrInvalidTree,
		},
		{
			name:  "unknown kind",
			files: []file{{path: "src/main.go", kind: inventory.Submodule + 1}},
			err:   inventory.ErrInvalidTree,
		},
		{
			name:  "duplicate path",
			files: []file{workflow, symlink(".github/workflows/ci.yml")},
			err:   inventory.ErrInvalidTree,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t, tc.files...)
			guard(tree, append(slices.Clone(tc.want), declaration))

			built, err := inventory.Build(context.Background(), tree)

			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Build error = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			requireManifest(t, built.Manifest(), tc.files, tc.want)
		})
	}
}

func TestBuildDigestIgnoresEntryOrder(t *testing.T) {
	files := []file{
		regular(declaration, "version: 1\ninclude: [/tests/**]\n"),
		regular("tests/b_test.go", "b"), regular("tests/a_test.go", "a"),
		regular(".github/workflows/ci.yml", "on: push\n"), regular("src/main.go", "m"),
	}
	reversed := slices.Clone(files)
	slices.Reverse(reversed)

	first, err := inventory.Build(context.Background(), newTree(t, files...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := inventory.Build(context.Background(), newTree(t, reversed...))
	if err != nil {
		t.Fatalf("Build reversed: %v", err)
	}
	if first.Manifest().Digest() != second.Manifest().Digest() || first.Scope().Digest() != second.Scope().Digest() {
		t.Fatal("inventories of the same tree differ by entry order")
	}
}

func TestBuildReadsTheDeclarationOnce(t *testing.T) {
	tree := newTree(t, regular(declaration, "version: 1\n"))

	if _, err := inventory.Build(context.Background(), tree); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !reflect.DeepEqual(tree.reads, []string{declaration}) {
		t.Fatalf("reads = %v, want the declaration once", tree.reads)
	}
}

func TestBuildWrapsTreeErrors(t *testing.T) {
	failure := errors.New("tree unavailable")
	tests := []struct {
		name  string
		files []file
		fail  func(*fakeTree)
	}{
		{"listing", nil, func(f *fakeTree) { f.entriesErr = failure }},
		{"reading the declaration", []file{regular(declaration, "version: 1\n")}, func(f *fakeTree) { f.readErr = failure }},
		{"reading a covered file", []file{regular(".github/workflows/ci.yml", "x")}, func(f *fakeTree) { f.readErr = failure }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t, tc.files...)
			tc.fail(tree)

			if _, err := inventory.Build(context.Background(), tree); !errors.Is(err, failure) {
				t.Fatalf("Build error = %v, want the tree's error wrapped", err)
			}
		})
	}
}

func TestInventoryContractBindsManifestAndScope(t *testing.T) {
	raw := "version: 1\ninclude: [/tests/**]\n"
	built, err := inventory.Build(context.Background(), newTree(t, regular(declaration, raw), regular("tests/a_test.go", "a")))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	parsed, err := scope.ParseDeclaration([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	wantScope := scope.NewScope(parsed)
	if built.Scope().Digest() != wantScope.Digest() {
		t.Fatal("inventory scope is not the scope of the tree's declaration")
	}
	want, err := contract.NewProtectedContract(built.Manifest(), wantScope.Digest(), map[string]string{})
	if err != nil {
		t.Fatalf("NewProtectedContract: %v", err)
	}

	got, err := built.Contract()

	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if !got.Equal(want) || len(got.CoveredInputs()) != 0 {
		t.Fatal("contract does not bind exactly the manifest and the scope digest")
	}
}

func TestEvaluate(t *testing.T) {
	governing := scopeOf(t, "version: 1\ninclude: [/tests/**]\n")
	tests := []struct {
		name  string
		files []file
		want  []string
		err   error
	}{
		{
			name: "governing rules decide coverage, not the tree's declaration",
			files: []file{
				regular(declaration, "not: [a valid declaration"), regular("tests/a_test.go", "a"),
				regular("Makefile", "m"), regular(".github/workflows/ci.yml", "w"),
			},
			want: []string{".github/workflows/ci.yml", declaration, "tests/a_test.go"},
		},
		{
			name:  "tree excluding governed files cannot drop them",
			files: []file{regular(declaration, "version: 1\nexclude: [/tests/**]\n"), regular("tests/a_test.go", "a")},
			want:  []string{declaration, "tests/a_test.go"},
		},
		{
			name:  "symlink inside the governing scope",
			files: []file{regular(declaration, "version: 1\n"), symlink("tests/link")},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "declaration as a symlink",
			files: []file{symlink(declaration)},
			err:   inventory.ErrUnsafeEntry,
		},
		{
			name:  "duplicate path",
			files: []file{regular("tests/a_test.go", "a"), regular("tests/a_test.go", "b")},
			err:   inventory.ErrInvalidTree,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t, tc.files...)
			guard(tree, tc.want)

			manifest, err := inventory.Evaluate(context.Background(), tree, governing)

			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Evaluate error = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			requireManifest(t, manifest, tc.files, tc.want)
		})
	}
}

func scopeOf(t *testing.T, raw string) scope.Scope {
	t.Helper()
	parsed, err := scope.ParseDeclaration([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	return scope.NewScope(parsed)
}

// baseFiles is the canonical tree the Classify cases start from.
func baseFiles() map[string]file {
	files := map[string]file{}
	for _, f := range []file{
		regular(declaration, "version: 1\ninclude: [/tests/**]\nrunner: [/Makefile]\n"),
		regular(".github/workflows/ci.yml", "on: push\n"),
		regular("tests/a_test.go", "a"), regular("tests/b_test.go", "b"),
		regular("Makefile", "test:\n"), regular("src/main.go", "package main\n"),
	} {
		files[f.path] = f
	}
	return files
}

// complete makes every list of want non-nil, so that a nil list in a result
// fails the comparison.
func complete(want inventory.Classification) inventory.Classification {
	for _, list := range []*[]string{&want.Added, &want.Removed, &want.Modified, &want.ReducedPaths, &want.ReducedRules} {
		*list = append([]string{}, *list...)
	}
	return want
}

func TestClassify(t *testing.T) {
	put := func(files map[string]file, f file) { files[f.path] = f }
	keep := func(map[string]file) {}
	tests := []struct {
		name   string
		change func(map[string]file)
		// governingScope, when set, replaces the governing scope's declaration.
		governingScope string
		absentManifest bool
		want           inventory.Classification
		err            error
	}{
		{
			name:   "identical tree",
			change: keep,
			want:   inventory.Classification{Change: inventory.Unchanged},
		},
		{
			name: "non-protected file changed",
			change: func(f map[string]file) {
				put(f, regular("src/main.go", "changed"))
				put(f, regular("src/new.go", "new"))
			},
			want: inventory.Classification{Change: inventory.Unchanged},
		},
		{
			name:   "protected file modified",
			change: func(f map[string]file) { put(f, regular("tests/a_test.go", "changed")) },
			want:   inventory.Classification{Change: inventory.ProtectedChange, Modified: []string{"tests/a_test.go"}},
		},
		{
			name:   "workflow modified",
			change: func(f map[string]file) { put(f, regular(".github/workflows/ci.yml", "on: pull_request\n")) },
			want:   inventory.Classification{Change: inventory.ProtectedChange, Modified: []string{".github/workflows/ci.yml"}},
		},
		{
			name:   "protected file added",
			change: func(f map[string]file) { put(f, regular("tests/c_test.go", "c")) },
			want:   inventory.Classification{Change: inventory.ProtectedChange, Added: []string{"tests/c_test.go"}},
		},
		{
			name:   "protected file removed",
			change: func(f map[string]file) { delete(f, "tests/b_test.go") },
			want:   inventory.Classification{Change: inventory.ProtectedChange, Removed: []string{"tests/b_test.go"}},
		},
		{
			name: "new include adds protection",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\ninclude: [/tests/**, /docs/**]\nrunner: [/Makefile]\n"))
				put(f, regular("docs/guide.md", "g"))
			},
			want: inventory.Classification{Change: inventory.ProtectedChange, Added: []string{"docs/guide.md"}, Modified: []string{declaration}},
		},
		{
			name:           "scope digest differs with the same inventory",
			change:         keep,
			governingScope: "version: 1\ninclude: [/tests/**]\nrunner: [/Makefile]\n# reviewed\n",
			want:           inventory.Classification{Change: inventory.ProtectedChange},
		},
		{
			name: "new exclude",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\ninclude: [/tests/**]\nexclude: [/tests/b_test.go]\nrunner: [/Makefile]\n"))
			},
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{"tests/b_test.go"}, Modified: []string{declaration},
				ReducedPaths: []string{"tests/b_test.go"}, ReducedRules: []string{"exclude /tests/b_test.go"},
			},
		},
		{
			name:   "removed include",
			change: func(f map[string]file) { put(f, regular(declaration, "version: 1\nrunner: [/Makefile]\n")) },
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{"tests/a_test.go", "tests/b_test.go"}, Modified: []string{declaration},
				ReducedPaths: []string{"tests/a_test.go", "tests/b_test.go"}, ReducedRules: []string{"rule /tests/**"},
			},
		},
		{
			name: "removed include and a deleted file it protected",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\nrunner: [/Makefile]\n"))
				delete(f, "tests/b_test.go")
			},
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{"tests/a_test.go", "tests/b_test.go"}, Modified: []string{declaration},
				ReducedPaths: []string{"tests/a_test.go", "tests/b_test.go"}, ReducedRules: []string{"rule /tests/**"},
			},
		},
		{
			name: "removed include with a new file the governing rules cover",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\nrunner: [/Makefile]\n"))
				put(f, regular("tests/c_test.go", "c"))
			},
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{"tests/a_test.go", "tests/b_test.go"}, Modified: []string{declaration},
				ReducedPaths: []string{"tests/a_test.go", "tests/b_test.go", "tests/c_test.go"}, ReducedRules: []string{"rule /tests/**"},
			},
		},
		{
			name:   "removed runner entry",
			change: func(f map[string]file) { put(f, regular(declaration, "version: 1\ninclude: [/tests/**]\n")) },
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{"Makefile"}, Modified: []string{declaration},
				ReducedPaths: []string{"Makefile"}, ReducedRules: []string{"rule /Makefile"},
			},
		},
		{
			name:   "deleted declaration while the governing scope had rules",
			change: func(f map[string]file) { delete(f, declaration) },
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Removed: []string{declaration, "Makefile", "tests/a_test.go", "tests/b_test.go"},
				ReducedPaths: []string{"Makefile", "tests/a_test.go", "tests/b_test.go"}, ReducedRules: []string{"rule /Makefile", "rule /tests/**"},
			},
		},
		{
			name: "excluding the declaration keeps it protected",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\ninclude: [/tests/**]\nexclude: [/.suiteward.yml]\nrunner: [/Makefile]\n"))
			},
			want: inventory.Classification{
				Change: inventory.ScopeReduction, Modified: []string{declaration}, ReducedRules: []string{"exclude /.suiteward.yml"},
			},
		},
		{
			name: "symlink the candidate drops from its scope stays unsafe",
			change: func(f map[string]file) {
				put(f, regular(declaration, "version: 1\nrunner: [/Makefile]\n"))
				put(f, symlink("tests/link"))
			},
			err: inventory.ErrUnsafeEntry,
		},
		{
			name:   "invalid candidate declaration",
			change: func(f map[string]file) { put(f, regular(declaration, "version: 1\nunknown: true\n")) },
			err:    scope.ErrInvalidDeclaration,
		},
		{
			name:           "absent governing manifest",
			change:         keep,
			absentManifest: true,
			err:            inventory.ErrInvalidGoverning,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, err := inventory.Build(context.Background(), treeOf(t, baseFiles()))
			if err != nil {
				t.Fatalf("Build base: %v", err)
			}
			governing := inventory.Governing{Scope: base.Scope(), Manifest: base.Manifest()}
			if tc.governingScope != "" {
				governing.Scope = scopeOf(t, tc.governingScope)
			}
			if tc.absentManifest {
				governing.Manifest = artifact.Manifest{}
			}
			files := baseFiles()
			tc.change(files)

			got, candidate, err := inventory.Classify(context.Background(), governing, treeOf(t, files))

			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Classify error = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if want := complete(tc.want); !reflect.DeepEqual(got, want) {
				t.Fatalf("classification = %+v, want %+v", got, want)
			}
			own, err := inventory.Build(context.Background(), treeOf(t, files))
			if err != nil {
				t.Fatalf("Build candidate: %v", err)
			}
			if candidate.Manifest().Digest() != own.Manifest().Digest() || candidate.Scope().Digest() != own.Scope().Digest() {
				t.Fatal("Classify did not return the candidate's own inventory")
			}
		})
	}
}
