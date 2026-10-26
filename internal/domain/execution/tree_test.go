package execution_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// manifestOf builds the manifest of files, a map of path to content.
func manifestOf(t *testing.T, files map[string]string) artifact.Manifest {
	t.Helper()
	var entries []artifact.Entry
	for path, content := range files {
		entries = append(entries, artifact.Entry{Path: path, Content: artifact.Hash([]byte(content))})
	}
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func patterns(t *testing.T, texts ...string) []scope.Pattern {
	t.Helper()
	var list []scope.Pattern
	for _, text := range texts {
		pattern, err := scope.ParsePattern(text)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, pattern)
	}
	return list
}

func file(path, content string, source execution.Source) execution.TreeFile {
	return execution.TreeFile{Path: path, Content: artifact.Hash([]byte(content)), Source: source}
}

// canonicalSelection treats test files and testdata as tests, except the legacy directory.
func canonicalSelection(t *testing.T) execution.Selection {
	t.Helper()
	return execution.Selection{Include: patterns(t, "*_test.go", "/testdata/"), Exclude: patterns(t, "/legacy/")}
}

// governingScope is the scope of a declaration that protects test files, testdata and the Makefile, besides the defaults
// (.suiteward.yml and the workflows), and excludes the legacy directory.
func governingScope(t *testing.T) scope.Scope {
	t.Helper()
	declaration, err := scope.ParseDeclaration([]byte("version: 1\ninclude:\n  - \"*_test.go\"\n  - /testdata/\nexclude:\n  - /legacy/\nrunner:\n  - /Makefile\n"))
	if err != nil {
		t.Fatal(err)
	}
	return scope.NewScope(declaration)
}

func TestSelectionMatches(t *testing.T) {
	sel := canonicalSelection(t)
	for path, want := range map[string]bool{
		"a_test.go": true, "pkg/deep/b_test.go": true, "testdata/x.json": true,
		"a.go": false, "pkg/testdata/x.json": false,
		"legacy/old_test.go": false, // matched by include, then removed by exclude
	} {
		if got, err := sel.Matches(path); err != nil || got != want {
			t.Errorf("Matches(%q) = %v, %v; want %v", path, got, err, want)
		}
	}
	if got, err := sel.Matches("../escape_test.go"); !errors.Is(err, artifact.ErrInvalidPath) {
		t.Errorf("an invalid path = %v, %v; want ErrInvalidPath", got, err)
	}
	if got, err := (execution.Selection{Include: []scope.Pattern{{}}}).Matches("a_test.go"); !errors.Is(err, scope.ErrInvalidPattern) {
		t.Errorf("a zero pattern = %v, %v; want ErrInvalidPattern", got, err)
	}
	if got, err := (execution.Selection{}).Matches("a_test.go"); err != nil || got {
		t.Errorf("an empty selection = %v, %v; want no match", got, err)
	}
}

// The canonical tree is the whole governing manifest over the head, and the head loses every file the governing scope
// covers and the manifest lacks. Head files outside the governing scope stay.
func TestCanonicalTreeOverlaysTheWholeGoverningManifestOnTheHead(t *testing.T) {
	head := manifestOf(t, map[string]string{
		"a.go":                         "source",
		"a_test.go":                    "head edit of a",
		"b_test.go":                    "b",
		"c_test.go":                    "added by the head, covered, not in the manifest",
		"testdata/x.json":              "head edit of x",
		"testdata/sub/deeper.txt":      "head deeper",
		"legacy/old_test.go":           "excluded, so outside the scope",
		"go.mod":                       "module head",
		".suiteward.yml":               "head declaration",
		"Makefile":                     "head edit of the runner file",
		".github/workflows/ci.yml":     "a workflow the manifest lacks",
		"pkg/testdata/keep.json":       "not under the top-level testdata",
		"internal/zeta_test.go":        "z head",
		"internal/zeta.go":             "z",
		"README.md":                    "readme",
		"scripts/run.sh":               "outside the scope, and not in the manifest",
		".github/workflows/extra.yml":  "another workflow the manifest lacks",
		"internal/unprotected_test.go": "covered by *_test.go, not in the manifest",
	})
	governing := manifestOf(t, map[string]string{
		"a_test.go":                     "canonical a",
		"b_test.go":                     "b",
		"d_test.go":                     "canonical d, which the head deleted",
		"testdata/x.json":               "canonical x",
		"testdata/sub/deeper.txt":       "canonical deeper",
		"internal/zeta_test.go":         "z canonical",
		".suiteward.yml":                "canonical declaration",
		"Makefile":                      "canonical runner file",
		".github/workflows/release.yml": "a workflow the head deleted",
	})
	got, err := execution.CanonicalTree(head, governing, governingScope(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []execution.TreeFile{
		file(".github/workflows/release.yml", "a workflow the head deleted", execution.FromManifest),
		file(".suiteward.yml", "canonical declaration", execution.FromManifest),
		file("Makefile", "canonical runner file", execution.FromManifest),
		file("README.md", "readme", execution.FromHead),
		file("a.go", "source", execution.FromHead),
		file("a_test.go", "canonical a", execution.FromManifest),
		file("b_test.go", "b", execution.FromManifest),
		// c_test.go, internal/unprotected_test.go and both workflows of the head are covered and absent from the manifest: removed.
		file("d_test.go", "canonical d, which the head deleted", execution.FromManifest),
		file("go.mod", "module head", execution.FromHead),
		file("internal/zeta.go", "z", execution.FromHead),
		file("internal/zeta_test.go", "z canonical", execution.FromManifest),
		file("legacy/old_test.go", "excluded, so outside the scope", execution.FromHead),
		file("pkg/testdata/keep.json", "not under the top-level testdata", execution.FromHead),
		file("scripts/run.sh", "outside the scope, and not in the manifest", execution.FromHead),
		file("testdata/sub/deeper.txt", "canonical deeper", execution.FromManifest),
		file("testdata/x.json", "canonical x", execution.FromManifest),
	}
	if !slices.Equal(got, want) {
		t.Errorf("tree =\n%v\nwant\n%v", got, want)
	}
}

func TestCanonicalTreeOverlaysAManifestFileTheScopeDoesNotCover(t *testing.T) {
	// A consented revision's manifest may hold a file its own declaration no longer covers: it is still the manifest.
	head := manifestOf(t, map[string]string{"notes.txt": "head"})
	governing := manifestOf(t, map[string]string{"notes.txt": "canonical"})
	got, err := execution.CanonicalTree(head, governing, governingScope(t))
	if want := []execution.TreeFile{file("notes.txt", "canonical", execution.FromManifest)}; err != nil || !slices.Equal(got, want) {
		t.Errorf("tree = %v, %v; want %v", got, err, want)
	}
}

func TestCanonicalTreeRefusesAnAbsentManifest(t *testing.T) {
	present := manifestOf(t, map[string]string{"a_test.go": "a"})
	if got, err := execution.CanonicalTree(artifact.Manifest{}, present, governingScope(t)); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent head = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.CanonicalTree(present, artifact.Manifest{}, governingScope(t)); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent governing manifest = %v, %v; want ErrInvalidTree", got, err)
	}
}

func TestCanonicalTreeOfAnEmptyHeadIsTheManifest(t *testing.T) {
	empty := manifestOf(t, nil)
	governing := manifestOf(t, map[string]string{"a_test.go": "a"})
	got, err := execution.CanonicalTree(empty, governing, governingScope(t))
	if want := []execution.TreeFile{file("a_test.go", "a", execution.FromManifest)}; err != nil || !slices.Equal(got, want) {
		t.Errorf("tree = %v, %v; want %v", got, err, want)
	}
}

func TestEvaluatedFilesAreAddedOrChangedRelativeToTheCanonical(t *testing.T) {
	head := manifestOf(t, map[string]string{
		"a_test.go":          "head edit of a",
		"b_test.go":          "b",
		"c_test.go":          "added by the head",
		"testdata/x.json":    "head edit of x",
		"a.go":               "source, changed but not a test file",
		"legacy/old_test.go": "excluded",
		"src/impl.go":        "source under a pattern only the head declares",
	})
	governing := manifestOf(t, map[string]string{
		"a_test.go":          "canonical a",
		"b_test.go":          "b",
		"d_test.go":          "deleted by the head: nothing to evaluate",
		"testdata/x.json":    "canonical x",
		"legacy/old_test.go": "canonical",
		"a.go":               "old source",
	})
	got, err := execution.EvaluatedFiles(head, governing, canonicalSelection(t))
	want := []string{"a_test.go", "c_test.go", "testdata/x.json"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("evaluated = %v, %v; want %v", got, err, want)
	}
}

func TestEvaluatedFilesOfAnUntouchedHeadIsEmpty(t *testing.T) {
	same := manifestOf(t, map[string]string{"a_test.go": "a", "a.go": "source"})
	got, err := execution.EvaluatedFiles(same, same, canonicalSelection(t))
	if err != nil || len(got) != 0 {
		t.Errorf("evaluated = %v, %v; want none", got, err)
	}
}

func TestEvaluatedFilesRefuseAnAbsentManifestAndABadPattern(t *testing.T) {
	present := manifestOf(t, map[string]string{"a_test.go": "a"})
	if got, err := execution.EvaluatedFiles(artifact.Manifest{}, present, canonicalSelection(t)); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent head = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.EvaluatedFiles(present, artifact.Manifest{}, canonicalSelection(t)); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent governing manifest = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.EvaluatedFiles(present, present, execution.Selection{Include: []scope.Pattern{{}}}); !errors.Is(err, scope.ErrInvalidPattern) {
		t.Errorf("a zero pattern = %v, %v; want ErrInvalidPattern", got, err)
	}
}

// The base side is the code before the change: the base commit, plus the head version of every file that the canonical
// include matches and that differs from the canonical version the revision expects. Test files, testdata and helpers in
// other test files all cross, and nothing else does.
func TestBaseSideTreeCarriesEveryIncludedFileThatDiffersFromTheExpectedCanonical(t *testing.T) {
	base := manifestOf(t, map[string]string{
		"a.go":                       "base source",
		"a_test.go":                  "base a test",
		"go.mod":                     "module base",
		"go.sum":                     "base sums",
		"src/impl.go":                "base impl",
		"old_test.go":                "deleted by the head, still in the base",
		"testdata/golden.txt":        "base golden",
		"internal/helpers_test.go":   "base helpers",
		"c_test.go":                  "older than the canonical the revision expects",
		"legacy/old_test.go":         "excluded",
		"internal/unchanged_test.go": "unchanged",
	})
	head := manifestOf(t, map[string]string{
		"a.go":                       "head source",
		"a_test.go":                  "head a test",
		"b_test.go":                  "head new test",
		"go.mod":                     "module head with a bump",
		"src/impl.go":                "head impl, under src/**, a pattern only the head declares",
		".suiteward.yml":             "head declaration that adds src/**",
		"testdata/golden.txt":        "head golden",
		"internal/helpers_test.go":   "base helpers",
		"internal/more_test.go":      "a new helper in another test file",
		"c_test.go":                  "the canonical the revision expects",
		"legacy/old_test.go":         "head edit of an excluded file",
		"internal/unchanged_test.go": "unchanged",
	})
	expected := manifestOf(t, map[string]string{
		"a_test.go":                  "base a test",
		"old_test.go":                "deleted by the head, still in the base",
		"testdata/golden.txt":        "base golden",
		"internal/helpers_test.go":   "base helpers",
		"c_test.go":                  "the canonical the revision expects",
		"internal/unchanged_test.go": "unchanged",
	})
	sel := canonicalSelection(t)

	evaluated, err := execution.EvaluatedFiles(head, expected, sel)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a_test.go", "b_test.go", "internal/more_test.go", "testdata/golden.txt"}; !slices.Equal(evaluated, want) {
		t.Fatalf("evaluated = %v, want %v: src/impl.go sits under a pattern the canonical does not have", evaluated, want)
	}
	got, err := execution.BaseSideTree(base, head, expected, sel)
	if err != nil {
		t.Fatal(err)
	}
	want := []execution.TreeFile{
		file("a.go", "base source", execution.FromBase),
		file("a_test.go", "head a test", execution.FromHead),
		file("b_test.go", "head new test", execution.FromHead),
		file("c_test.go", "older than the canonical the revision expects", execution.FromBase), // equal to the expected canonical in the head: not carried
		file("go.mod", "module base", execution.FromBase),
		file("go.sum", "base sums", execution.FromBase),
		file("internal/helpers_test.go", "base helpers", execution.FromBase),
		file("internal/more_test.go", "a new helper in another test file", execution.FromHead),
		file("internal/unchanged_test.go", "unchanged", execution.FromBase),
		file("legacy/old_test.go", "excluded", execution.FromBase),
		file("old_test.go", "deleted by the head, still in the base", execution.FromBase),
		file("src/impl.go", "base impl", execution.FromBase), // the source file stays at its base bytes
		file("testdata/golden.txt", "head golden", execution.FromHead),
	}
	if !slices.Equal(got, want) {
		t.Errorf("tree =\n%v\nwant\n%v", got, want)
	}
}

func TestBaseSideTreeRefusesAnAbsentManifestAndABadPattern(t *testing.T) {
	present := manifestOf(t, map[string]string{"a_test.go": "a"})
	sel := canonicalSelection(t)
	for name, manifests := range map[string][3]artifact.Manifest{
		"an absent base":     {{}, present, present},
		"an absent head":     {present, {}, present},
		"an absent expected": {present, present, {}},
	} {
		if got, err := execution.BaseSideTree(manifests[0], manifests[1], manifests[2], sel); !errors.Is(err, execution.ErrInvalidTree) {
			t.Errorf("%s = %v, %v; want ErrInvalidTree", name, got, err)
		}
	}
	changed := manifestOf(t, map[string]string{"a_test.go": "changed"})
	bad := execution.Selection{Include: []scope.Pattern{{}}}
	if got, err := execution.BaseSideTree(present, changed, present, bad); !errors.Is(err, scope.ErrInvalidPattern) {
		t.Errorf("a zero pattern = %v, %v; want ErrInvalidPattern", got, err)
	}
}

func TestBaseSideTreeOfAHeadThatChangesNoIncludedFileIsTheBase(t *testing.T) {
	base := manifestOf(t, map[string]string{"b.go": "b", "a.go": "a", "a_test.go": "t"})
	head := manifestOf(t, map[string]string{"a.go": "changed", "a_test.go": "t"})
	expected := manifestOf(t, map[string]string{"a_test.go": "t"})
	got, err := execution.BaseSideTree(base, head, expected, canonicalSelection(t))
	want := []execution.TreeFile{file("a.go", "a", execution.FromBase), file("a_test.go", "t", execution.FromBase), file("b.go", "b", execution.FromBase)}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("tree = %v, %v; want %v", got, err, want)
	}
}

// A composed tree is a directory tree: no path may sit below another path of it, as the head's file x and the manifest's
// x/y would make one. The check is by set, because the sort order does not put a file next to its descendants ("a",
// "a.txt", "a/b").
func TestCanonicalTreeRefusesAPathBelowAnotherPathOfTheTree(t *testing.T) {
	tests := []struct {
		name      string
		head      map[string]string
		governing map[string]string
	}{
		{"a head file and a manifest path below it", map[string]string{"tests": "a symlink"}, map[string]string{"tests/a_test.go": "t"}},
		{"far below", map[string]string{"a/b": "a file"}, map[string]string{"a/b/c/d_test.go": "t"}},
		{"not next to it in path order", map[string]string{"a": "a file", "a.txt": "x"}, map[string]string{"a/b_test.go": "t"}},
		{"a manifest file and a head path below it", map[string]string{"docs/x.txt": "x"}, map[string]string{"docs": "a file"}},
		{"two manifest paths", map[string]string{"go.mod": "m"}, map[string]string{"a_test.go": "t", "a_test.go/b": "x"}},
		// A filesystem that ignores case, and drops trailing dots and spaces, sees these as one name.
		{"a head file and a manifest path below it in another case", map[string]string{"Tests": "a symlink"}, map[string]string{"tests/a_test.go": "t"}},
		{"a head file spelled with a dot", map[string]string{"d.": "a symlink"}, map[string]string{"d/x_test.go": "t"}},
		{"a manifest file and a head path below it in another case", map[string]string{"docs/x.txt": "x"}, map[string]string{"Docs": "a file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := execution.CanonicalTree(manifestOf(t, tt.head), manifestOf(t, tt.governing), governingScope(t))
			if !errors.Is(err, execution.ErrInvalidTree) {
				t.Errorf("CanonicalTree = %v, %v; want ErrInvalidTree", got, err)
			}
		})
	}
}

func TestBaseSideTreeRefusesAPathBelowAnotherPathOfTheTree(t *testing.T) {
	tests := []struct {
		name string
		base map[string]string
		head map[string]string
	}{
		{"a base file and a carried path below it", map[string]string{"x": "a file"}, map[string]string{"x/y_test.go": "t"}},
		{"a carried file and a base path below it", map[string]string{"x_test.go/inner.go": "i"}, map[string]string{"x_test.go": "a file"}},
		{"not next to it in path order", map[string]string{"a": "a file", "a.txt": "x"}, map[string]string{"a/b_test.go": "t"}},
		{"a base file and a carried path below it in another case", map[string]string{"X": "a file"}, map[string]string{"x/y_test.go": "t"}},
		{"a carried file and a base path below it in another case", map[string]string{"X_TEST.go/inner.go": "i"}, map[string]string{"x_test.go": "a file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := manifestOf(t, map[string]string{"keep_test.go": "k"})
			got, err := execution.BaseSideTree(manifestOf(t, tt.base), manifestOf(t, tt.head), expected, canonicalSelection(t))
			if !errors.Is(err, execution.ErrInvalidTree) {
				t.Errorf("BaseSideTree = %v, %v; want ErrInvalidTree", got, err)
			}
		})
	}
}
