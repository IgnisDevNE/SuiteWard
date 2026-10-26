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

func TestCanonicalTreeOverlaysTheGoverningManifestOnTheHead(t *testing.T) {
	head := manifestOf(t, map[string]string{
		"a.go":                    "source",
		"a_test.go":               "head edit of a",
		"b_test.go":               "b",
		"c_test.go":               "added by the head",
		"testdata/x.json":         "head edit of x",
		"legacy/old_test.go":      "excluded, so a head file like any other",
		"go.mod":                  "module head",
		".suiteward.yml":          "head declaration",
		"pkg/testdata/keep.json":  "not under the top-level testdata",
		"internal/zeta_test.go":   "z head",
		"internal/zeta.go":        "z",
		"README.md":               "readme",
		"testdata/sub/deeper.txt": "head deeper",
	})
	governing := manifestOf(t, map[string]string{
		"a_test.go":               "canonical a",
		"b_test.go":               "b",
		"d_test.go":               "canonical d, which the head deleted",
		"testdata/x.json":         "canonical x",
		"testdata/sub/deeper.txt": "canonical deeper",
		"internal/zeta_test.go":   "z canonical",
		".suiteward.yml":          "canonical declaration",
		"Makefile":                "a runner file the head deleted",
	})
	got, err := execution.CanonicalTree(head, governing, canonicalSelection(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []execution.TreeFile{
		file(".suiteward.yml", "head declaration", execution.FromHead), // not matched by the selection: kept, even though the manifest holds one
		file("Makefile", "a runner file the head deleted", execution.FromManifest),
		file("README.md", "readme", execution.FromHead),
		file("a.go", "source", execution.FromHead),
		file("a_test.go", "canonical a", execution.FromManifest),
		file("b_test.go", "b", execution.FromManifest),
		// c_test.go is matched and absent from the manifest: removed.
		file("d_test.go", "canonical d, which the head deleted", execution.FromManifest),
		file("go.mod", "module head", execution.FromHead),
		file("internal/zeta.go", "z", execution.FromHead),
		file("internal/zeta_test.go", "z canonical", execution.FromManifest),
		file("legacy/old_test.go", "excluded, so a head file like any other", execution.FromHead),
		file("pkg/testdata/keep.json", "not under the top-level testdata", execution.FromHead),
		file("testdata/sub/deeper.txt", "canonical deeper", execution.FromManifest),
		file("testdata/x.json", "canonical x", execution.FromManifest),
	}
	if !slices.Equal(got, want) {
		t.Errorf("tree =\n%v\nwant\n%v", got, want)
	}
}

func TestCanonicalTreeRefusesAnAbsentManifestAndABadPattern(t *testing.T) {
	present := manifestOf(t, map[string]string{"a_test.go": "a"})
	sel := canonicalSelection(t)
	if got, err := execution.CanonicalTree(artifact.Manifest{}, present, sel); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent head = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.CanonicalTree(present, artifact.Manifest{}, sel); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent governing manifest = %v, %v; want ErrInvalidTree", got, err)
	}
	bad := execution.Selection{Include: []scope.Pattern{{}}}
	if got, err := execution.CanonicalTree(present, present, bad); !errors.Is(err, scope.ErrInvalidPattern) {
		t.Errorf("a zero pattern = %v, %v; want ErrInvalidPattern", got, err)
	}
}

func TestCanonicalTreeOfAnEmptyHeadIsTheManifest(t *testing.T) {
	empty := manifestOf(t, nil)
	governing := manifestOf(t, map[string]string{"a_test.go": "a"})
	got, err := execution.CanonicalTree(empty, governing, canonicalSelection(t))
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

// The base side is the code before the change: the base commit, plus the head versions of the evaluated test files.
func TestBaseSideTreeCarriesOnlyTheEvaluatedTestFiles(t *testing.T) {
	base := manifestOf(t, map[string]string{
		"a.go":        "base source",
		"a_test.go":   "base a test",
		"go.mod":      "module base",
		"go.sum":      "base sums",
		"src/impl.go": "base impl",
		"old_test.go": "deleted by the head, still in the base",
	})
	head := manifestOf(t, map[string]string{
		"a.go":           "head source",
		"a_test.go":      "head a test",
		"b_test.go":      "head new test",
		"go.mod":         "module head with a bump",
		"src/impl.go":    "head impl, under src/**, a pattern only the head declares",
		".suiteward.yml": "head declaration that adds src/**",
	})
	governing := manifestOf(t, map[string]string{"a_test.go": "base a test", "old_test.go": "deleted by the head, still in the base"})
	sel := canonicalSelection(t)

	evaluated, err := execution.EvaluatedFiles(head, governing, sel)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a_test.go", "b_test.go"}; !slices.Equal(evaluated, want) {
		t.Fatalf("evaluated = %v, want %v: src/impl.go sits under a pattern the canonical does not have", evaluated, want)
	}
	got, err := execution.BaseSideTree(base, head, sel, evaluated)
	if err != nil {
		t.Fatal(err)
	}
	want := []execution.TreeFile{
		file("a.go", "base source", execution.FromBase),
		file("a_test.go", "head a test", execution.FromHead),
		file("b_test.go", "head new test", execution.FromHead),
		file("go.mod", "module base", execution.FromBase),
		file("go.sum", "base sums", execution.FromBase),
		file("old_test.go", "deleted by the head, still in the base", execution.FromBase),
		file("src/impl.go", "base impl", execution.FromBase), // the source file stays at its base bytes
	}
	if !slices.Equal(got, want) {
		t.Errorf("tree =\n%v\nwant\n%v", got, want)
	}
}

func TestBaseSideTreeRefusesToCarryAFileTheCanonicalDoesNotSelect(t *testing.T) {
	base := manifestOf(t, map[string]string{"src/impl.go": "base impl", "a_test.go": "a"})
	head := manifestOf(t, map[string]string{"src/impl.go": "head impl", "a_test.go": "a", "legacy/old_test.go": "excluded"})
	sel := canonicalSelection(t)
	for _, path := range []string{"src/impl.go", "legacy/old_test.go"} {
		if got, err := execution.BaseSideTree(base, head, sel, []string{path}); !errors.Is(err, execution.ErrInvalidTree) {
			t.Errorf("carrying %q = %v, %v; want ErrInvalidTree", path, got, err)
		}
	}
	if got, err := execution.BaseSideTree(base, head, sel, []string{"ghost_test.go"}); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("carrying a file the head lacks = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.BaseSideTree(base, head, sel, []string{"../x_test.go"}); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("carrying an invalid path = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.BaseSideTree(artifact.Manifest{}, head, sel, nil); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent base = %v, %v; want ErrInvalidTree", got, err)
	}
	if got, err := execution.BaseSideTree(base, artifact.Manifest{}, sel, nil); !errors.Is(err, execution.ErrInvalidTree) {
		t.Errorf("an absent head = %v, %v; want ErrInvalidTree", got, err)
	}
	bad := execution.Selection{Include: []scope.Pattern{{}}}
	if got, err := execution.BaseSideTree(base, head, bad, []string{"a_test.go"}); !errors.Is(err, scope.ErrInvalidPattern) {
		t.Errorf("a zero pattern = %v, %v; want ErrInvalidPattern", got, err)
	}
}

func TestBaseSideTreeWithNothingCarriedIsTheBase(t *testing.T) {
	base := manifestOf(t, map[string]string{"b.go": "b", "a.go": "a"})
	head := manifestOf(t, map[string]string{"a.go": "changed"})
	got, err := execution.BaseSideTree(base, head, canonicalSelection(t), nil)
	want := []execution.TreeFile{file("a.go", "a", execution.FromBase), file("b.go", "b", execution.FromBase)}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("tree = %v, %v; want %v", got, err, want)
	}
}

func TestBaseSideTreeCarriesAFileOnceHoweverOftenItIsListed(t *testing.T) {
	base := manifestOf(t, map[string]string{"a_test.go": "old"})
	head := manifestOf(t, map[string]string{"a_test.go": "new"})
	got, err := execution.BaseSideTree(base, head, canonicalSelection(t), []string{"a_test.go", "a_test.go"})
	if want := []execution.TreeFile{file("a_test.go", "new", execution.FromHead)}; err != nil || !slices.Equal(got, want) {
		t.Errorf("tree = %v, %v; want %v", got, err, want)
	}
}
