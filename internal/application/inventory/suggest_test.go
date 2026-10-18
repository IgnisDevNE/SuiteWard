package inventory_test

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

func entriesOf(files ...file) []inventory.Entry {
	entries := make([]inventory.Entry, 0, len(files))
	for _, f := range files {
		entries = append(entries, inventory.Entry{Path: f.path, Kind: f.kind})
	}
	return entries
}

var none = []string{}

func regulars(names ...string) []file {
	files := make([]file, 0, len(names))
	for _, name := range names {
		files = append(files, regular(name, ""))
	}
	return files
}

// allIncludeSources holds one file for each include pattern the detector knows.
func allIncludeSources() []file {
	return regulars(
		"a/testdata/x", "a/__tests__/x", "a/__snapshots__/x", "a/__mocks__/x", "a/__fixtures__/x", "a/cypress/x",
		"a/src/test/x", "a/src/it/x", "tests/x", "test/x", "spec/x",
		"a/x.cy.ts", "a/x.spec.ts", "a/x.test.ts", "a/XTest.java", "a/XTest.kt", "a/XTest.php", "a/XTests.cs", "a/XTests.java",
		"a/x_spec.rb", "a/x_test.dart", "a/x_test.exs", "a/x_test.go", "a/x_test.py", "a/x_test.rb", "a/tests.py", "a/test_x.py",
	)
}

var pytestGroup = []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}

var suggestCases = []struct {
	name  string
	files []file
	want  inventory.Suggestion
}{
	{"empty input", nil, inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
	{"a Jest monorepo", regulars("packages/a/src/x.test.ts", "packages/a/__tests__/y.js", "packages/a/jest.config.js"),
		inventory.Suggestion{Submodules: none, Include: []string{"__tests__/", "*.test.*"}, Runner: []string{"jest.config.*"}, Mixed: none}},
	{"a Django app", regulars("app/tests.py"),
		inventory.Suggestion{Submodules: none, Include: []string{"tests.py"}, Runner: pytestGroup, Mixed: none}},
	{"a Maven module", regulars("module/src/test/java/FooTest.java", "module/src/test/resources/f.json"),
		inventory.Suggestion{Submodules: none, Include: []string{"**/src/test/", "*Test.java"}, Runner: none, Mixed: none}},
	{"a Rails app", regulars("spec/models/user_spec.rb", ".rspec"),
		inventory.Suggestion{Submodules: none, Include: []string{"/spec/", "*_spec.rb"}, Runner: []string{".rspec", "rails_helper.rb", "spec_helper.rb"}, Mixed: none}},
	{"a Python repo without config gets the whole pytest group", regulars("pkg/test_a.py", "pkg/b_test.py"),
		inventory.Suggestion{Submodules: none, Include: []string{"*_test.py", "test_*.py"}, Runner: pytestGroup, Mixed: none}},
	{"a Python test in a vendored directory does not trigger pytest", regulars(".venv/lib/test_a.py", "a/venv/b_test.py"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
	{"one pytest member brings the whole group", regulars("src/conftest.py"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: pytestGroup, Mixed: none}},
	{"one RSpec member brings the whole group", regulars(".rspec"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: []string{".rspec", "rails_helper.rb", "spec_helper.rb"}, Mixed: none}},
	{"the other runner groups", regulars("a/vitest.workspace.ts", "b/.mocharc.json", "karma.conf.js", "playwright.config.ts", "cypress.config.ts",
		".nycrc.json", "phpunit.xml.dist", "test_helper.exs", "dart_test.yaml", ".codecov.yml"),
		inventory.Suggestion{Submodules: none, Include: none, Mixed: none, Runner: []string{
			".c8rc*", ".codecov.yml", ".coveragerc", ".mocharc.*", ".nycrc*", "codecov.yml", "cypress.config.*", "dart_test.yaml", "karma.conf.*",
			"phpunit.dist.xml", "phpunit.xml", "phpunit.xml.dist", "playwright.config.*", "test_helper.*", "vitest.config.*", "vitest.workspace.*"}}},
	{"vendored paths are ignored everywhere", regulars(
		"node_modules/x/x.test.js", "a/node_modules/b/__tests__/c.js", "vendor/x_test.go", "bower_components/y/z.spec.js", "jspm_packages/a_test.go",
		".tox/a/test_b.py", "__pycache__/a.test.py", ".git/hooks/x_test.go", "node_modules/jest/package.json", "node_modules/a/jest.config.js"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
	{"a nested tests directory counts only for a test-named file", regulars("pkg/tests/a.txt", "pkg/test/b.txt", "pkg/spec/c.txt", "tests"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
	{"a nested tests directory holding a test-named file", regulars("pkg/tests/a_test.go", "pkg/test/b.test.ts", "pkg/spec/c_spec.rb"),
		inventory.Suggestion{Submodules: none, Include: []string{"spec/", "test/", "tests/", "*.test.*", "*_spec.rb", "*_test.go"}, Runner: none, Mixed: none}},
	{"a top-level tests directory with any file", regulars("tests/a.txt", "test/b/c.txt", "spec/d"),
		inventory.Suggestion{Submodules: none, Include: []string{"/spec/", "/test/", "/tests/"}, Runner: none, Mixed: none}},
	{"directories, symlinks and submodules are ignored", []file{symlink("a_test.go"), submodule("testdata/x"), symlink("jest.config.js"), submodule("package.json")},
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
	{"mixed files are listed, sorted, and never suggested", regulars(
		"package.json", "a/pyproject.toml", "x/y.csproj", "Cargo.toml", "node_modules/z/package.json", "tox.ini", "setup.cfg", "CMakeLists.txt", "go.mod"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: []string{
			"CMakeLists.txt", "Cargo.toml", "a/pyproject.toml", "package.json", "setup.cfg", "tox.ini", "x/y.csproj"}}},
	{"names that look like a convention but are not", regulars(
		"a/Foo.java", "src/testx/a.txt", "a/src/b/test/c.txt", "a/contests.py", "a/test_a.go", "a/testing.py", "a/XTest.rb", "x/attest.go", "b/cypress.json"),
		inventory.Suggestion{Submodules: none, Include: none, Runner: none, Mixed: none}},
}

func TestSuggest(t *testing.T) {
	for _, tt := range suggestCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := inventory.Suggest(entriesOf(tt.files...)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Suggest = %#v, want %#v", got, tt.want)
			}
			reversed := slices.Clone(tt.files)
			slices.Reverse(reversed)
			if got := inventory.Suggest(entriesOf(reversed...)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Suggest in reverse order = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSuggestFollowsEveryConvention(t *testing.T) {
	tests := map[string]string{
		"pkg/foo_test.go": "*_test.go", "a/test_x.py": "test_*.py", "a/x_test.py": "*_test.py", "a/tests.py": "tests.py",
		"web/a.test.ts": "*.test.*", "web/a.spec.ts": "*.spec.*", "web/a.cy.ts": "*.cy.*",
		"a/x_spec.rb": "*_spec.rb", "a/x_test.rb": "*_test.rb", "a/x_test.exs": "*_test.exs", "a/x_test.dart": "*_test.dart",
		"a/FooTest.java": "*Test.java", "a/FooTests.java": "*Tests.java", "a/FooTest.kt": "*Test.kt", "a/FooTests.cs": "*Tests.cs", "a/FooTest.php": "*Test.php",
		"testdata/x": "testdata/", "a/b/testdata/x": "testdata/", "a/__tests__/x": "__tests__/", "a/__snapshots__/x": "__snapshots__/",
		"a/__mocks__/x": "__mocks__/", "a/__fixtures__/x": "__fixtures__/", "cypress/e2e/x.ts": "cypress/",
		"src/test/X.java": "**/src/test/", "a/b/src/it/X.java": "**/src/it/",
	}
	for source, want := range tests {
		t.Run(source, func(t *testing.T) {
			got := inventory.Suggest(entriesOf(regular(source, "")))
			if !slices.Contains(got.Include, want) {
				t.Fatalf("Suggest(%q).Include = %v, want %q in it", source, got.Include, want)
			}
		})
	}
}

func TestSuggestCapsIncludeAndRunnerTogether(t *testing.T) {
	directories := slices.Sorted(slices.Values([]string{
		"__fixtures__/", "__mocks__/", "__snapshots__/", "__tests__/", "cypress/", "testdata/", "**/src/it/", "**/src/test/", "/spec/", "/test/", "/tests/",
	}))
	files := slices.Sorted(slices.Values([]string{
		"*.cy.*", "*.spec.*", "*.test.*", "*Test.java", "*Test.kt", "*Test.php", "*Tests.cs", "*Tests.java",
		"*_spec.rb", "*_test.dart", "*_test.exs", "*_test.go", "*_test.py", "*_test.rb", "tests.py", "test_*.py",
	}))
	t.Run("directories, then files, then the cut", func(t *testing.T) {
		got := inventory.Suggest(entriesOf(allIncludeSources()...))
		want := inventory.Suggestion{Submodules: none, Include: slices.Concat(directories, files)[:20], Runner: none, Mixed: none, Cut: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Suggest = %#v, want %#v", got, want)
		}
	})
	base := slices.Concat(
		regulars("a/testdata/x", "a/__tests__/x", "a/__snapshots__/x", "a/__mocks__/x", "a/__fixtures__/x", "a/cypress/x", "a/src/test/x", "a/src/it/x", "tests/x", "test/x", "spec/x"),
		regulars("a/x.cy.ts", "a/x.spec.ts", "a/x.test.ts", "a/x_test.go"),               // 11 directories and 4 files: 15 patterns
		regulars("jest.config.js", "vitest.config.ts", "a/.mocharc.js", "karma.conf.js"), // 5 runner patterns: exactly 20
	)
	t.Run("exactly twenty is not a cut", func(t *testing.T) {
		got := inventory.Suggest(entriesOf(base...))
		if len(got.Include) != 15 || len(got.Runner) != 5 || got.Cut {
			t.Fatalf("Suggest = %#v, want 15 include, 5 runner and no cut", got)
		}
	})
	t.Run("the runner patterns are cut first, in sorted order", func(t *testing.T) {
		got := inventory.Suggest(entriesOf(append(slices.Clone(base), regulars("playwright.config.ts")...)...))
		wantRunner := []string{".mocharc.*", "jest.config.*", "karma.conf.*", "playwright.config.*", "vitest.config.*"} // vitest.workspace.* is dropped
		if !reflect.DeepEqual(got.Runner, wantRunner) || len(got.Include) != 15 || !got.Cut {
			t.Fatalf("Suggest = %#v, want 15 include patterns, the first 5 runner patterns and a cut", got)
		}
	})
}

func TestSuggestMixedHasItsOwnLimit(t *testing.T) {
	var files []file
	var want []string
	for i := 0; i < 25; i++ {
		files = append(files, regular(fmt.Sprintf("d%02d/package.json", i), ""))
		if i < 20 {
			want = append(want, fmt.Sprintf("d%02d/package.json", i))
		}
	}
	got := inventory.Suggest(entriesOf(files...))
	if !reflect.DeepEqual(got.Mixed, want) || got.Cut {
		t.Fatalf("Suggest = %#v, want the first 20 mixed paths and no cut", got)
	}
}

func TestSuggestPatternsParseAndIncludeOnesMatchAFile(t *testing.T) {
	for _, tt := range suggestCases {
		t.Run(tt.name, func(t *testing.T) {
			got := inventory.Suggest(entriesOf(tt.files...))
			for _, pattern := range slices.Concat(got.Include, got.Runner) {
				if _, err := scope.ParsePattern(pattern); err != nil {
					t.Fatalf("ParsePattern(%q): %v", pattern, err)
				}
			}
			for _, pattern := range got.Include { // a runner pattern may match nothing: it is a higher-priority name
				p, _ := scope.ParsePattern(pattern)
				if !slices.ContainsFunc(tt.files, func(f file) bool { ok, _ := p.Match(f.path); return ok }) {
					t.Errorf("include pattern %q matches no input file", pattern)
				}
			}
		})
	}
}

func suggestionScope(t *testing.T, s inventory.Suggestion) scope.Scope {
	t.Helper()
	raw, err := scope.AddPatterns([]byte("version: 1\n"), s.Include, s.Runner)
	if err != nil {
		t.Fatalf("AddPatterns: %v", err)
	}
	declaration, err := scope.ParseDeclaration(raw)
	if err != nil {
		t.Fatalf("ParseDeclaration(%q): %v", raw, err)
	}
	return scope.NewScope(declaration)
}

func suiteWardPaths(t *testing.T) []inventory.Entry {
	t.Helper()
	raw, err := os.ReadFile("testdata/suiteward_paths.txt")
	if err != nil {
		t.Fatal(err)
	}
	var entries []inventory.Entry
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			entries = append(entries, inventory.Entry{Path: line, Kind: inventory.File})
		}
	}
	return entries
}

// TestSuggestCoversSuiteWardsOwnTests is the C2 regression (M1.8), where 137 of 209 protected files were covered.
// The fixture is the output of git ls-files at 8346ee8.
func TestSuggestCoversSuiteWardsOwnTests(t *testing.T) {
	entries := suiteWardPaths(t)
	got := inventory.Suggest(entries)
	if got.Cut {
		t.Fatalf("Suggest cut the list: %v %v", got.Include, got.Runner)
	}
	covering := suggestionScope(t, got)
	checked := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Path, "_test.go") && !strings.Contains("/"+entry.Path, "/testdata/") && entry.Path != "codecov.yml" {
			continue
		}
		checked++
		if ok, err := covering.Covers(entry.Path); err != nil || !ok {
			t.Errorf("Covers(%q) = %v, %v; want true", entry.Path, ok, err)
		}
	}
	if checked < 150 {
		t.Fatalf("checked %d paths, the fixture lost its tests", checked)
	}
	// Expected misses, by design: the path-only detector knows no convention for PowerShell scripts or linter configuration.
	// This pins the documented limit, so it cannot drift silently; when a rule starts covering them, update the documentation too.
	missed := 0
	for _, entry := range entries {
		if (strings.HasPrefix(entry.Path, "scripts/") && strings.HasSuffix(entry.Path, ".ps1")) || entry.Path == ".golangci.yml" {
			missed++
			if ok, err := covering.Covers(entry.Path); err != nil || ok {
				t.Errorf("Covers(%q) = %v, %v; want the documented miss", entry.Path, ok, err)
			}
		}
	}
	if missed < 2 {
		t.Fatalf("found %d expected misses in the fixture, want scripts/*.ps1 and .golangci.yml", missed)
	}
}

// TestUncoveredAgreesWithSuggest: a scope made of the suggestion leaves no test-like or runner path uncovered.
func TestUncoveredAgreesWithSuggest(t *testing.T) {
	fixtures := map[string][]inventory.Entry{"SuiteWard": suiteWardPaths(t)}
	for _, tt := range suggestCases {
		fixtures[tt.name] = entriesOf(tt.files...)
	}
	for name, entries := range fixtures {
		t.Run(name, func(t *testing.T) {
			suggestion := inventory.Suggest(entries)
			if suggestion.Cut {
				t.Skip("a cut suggestion leaves paths uncovered by design")
			}
			got, err := inventory.Uncovered(entries, suggestionScope(t, suggestion))
			if err != nil || !reflect.DeepEqual(got, none) {
				t.Fatalf("Uncovered = %v, %v; want none", got, err)
			}
		})
	}
}

func scopeFrom(t *testing.T, raw string) scope.Scope {
	t.Helper()
	d := scope.NoDeclaration()
	if raw != "" {
		var err error
		if d, err = scope.ParseDeclaration([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return scope.NewScope(d)
}

func TestUncovered(t *testing.T) {
	tests := []struct {
		name        string
		declaration string
		files       []file
		want        []string
	}{
		{"empty input", "", nil, none},
		{"test-like and runner paths, sorted", "", regulars("main.go", "z/b_test.go", "a/testdata/x.json", "README.md", "jest.config.js", "package.json", "a/c.spec.ts"),
			[]string{"a/c.spec.ts", "a/testdata/x.json", "jest.config.js", "z/b_test.go"}},
		{"covered paths are left out", "version: 1\ninclude: ['**/*_test.go']\nrunner: [jest.config.*]\n", regulars("a_test.go", "pkg/b_test.go", "c.test.ts", "jest.config.js"),
			[]string{"c.test.ts"}},
		{"the defaults are covered", "", regulars(".github/workflows/x_test.go", "x/.github/workflows/y_test.go"), []string{"x/.github/workflows/y_test.go"}},
		{"an excluded path is never reported", "version: 1\ninclude: [docs/]\nexclude: [gen/, '*.generated_test.go']\n",
			regulars("gen/a_test.go", "pkg/x.generated_test.go", "pkg/y_test.go", "docs/z_test.go"), []string{"pkg/y_test.go"}},
		{"vendored paths and non-files are never reported", "", append(regulars("node_modules/a/x.test.js", "vendor/y_test.go", ".venv/z/test_a.py"), symlink("a_test.go"), submodule("testdata/s")), none},
		{"a new root conftest.py outside the scope", "version: 1\ninclude: ['**/test_*.py']\n", regulars("conftest.py", "tests/test_a.py", "pkg/pytest.toml"), []string{"conftest.py", "pkg/pytest.toml"}},
		{"every runner group is reported", "", regulars(
			"pytest.ini", "a/.pytest.toml", "jest.config.ts", "vitest.config.ts", "vitest.workspace.ts", ".mocharc.json", "karma.conf.js", "playwright.config.ts",
			"cypress.config.ts", ".coveragerc", ".nycrc.json", ".c8rc.json", "phpunit.xml", "phpunit.xml.dist", "phpunit.dist.xml", ".rspec", "spec_helper.rb",
			"rails_helper.rb", "test_helper.exs", "dart_test.yaml", "codecov.yml", ".codecov.yml", "package.json", "tox.ini"),
			[]string{
				".c8rc.json", ".codecov.yml", ".coveragerc", ".mocharc.json", ".nycrc.json", ".rspec", "a/.pytest.toml", "codecov.yml", "cypress.config.ts", "dart_test.yaml",
				"jest.config.ts", "karma.conf.js", "phpunit.dist.xml", "phpunit.xml", "phpunit.xml.dist", "playwright.config.ts", "pytest.ini", "rails_helper.rb",
				"spec_helper.rb", "test_helper.exs", "vitest.config.ts", "vitest.workspace.ts"}},
		{"an excluded or vendored runner file is never reported", "version: 1\nexclude: [legacy/]\n", regulars("legacy/conftest.py", "node_modules/x/jest.config.js", ".venv/conftest.py", "conftest.py"), []string{"conftest.py"}},
		{"a nested tests directory needs a test-named file", "", regulars("pkg/tests/data.txt", "tests/data.txt", "pkg/tests/a_test.go"), []string{"pkg/tests/a_test.go", "tests/data.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inventory.Uncovered(entriesOf(tt.files...), scopeFrom(t, tt.declaration))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Uncovered = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestUncoveredRejectsAnInvalidPath(t *testing.T) {
	_, err := inventory.Uncovered(entriesOf(regular("../x_test.go", "")), scopeFrom(t, ""))
	if !errors.Is(err, artifact.ErrInvalidPath) {
		t.Fatalf("Uncovered error = %v, want ErrInvalidPath", err)
	}
}

func TestSuggestReportsTheSubmodulesItsPatternsReach(t *testing.T) {
	tests := []struct {
		name  string
		files []file
		want  []string
	}{
		{"an unanchored pattern reaches every submodule, sorted", append(regulars("a_test.go"), submodule("z/lib"), submodule("third_party/a")), []string{"third_party/a", "z/lib"}},
		{"a runner pattern reaches them too", append(regulars("jest.config.js"), submodule("lib")), []string{"lib"}},
		{"vendored submodules are left out", append(regulars("a_test.go"), submodule("vendor/lib"), submodule("a/node_modules/x"), submodule(".git/modules")), none},
		{"no pattern, no submodule to warn about", []file{submodule("lib")}, none},
		{"an anchored pattern reaches only the submodules under it", append(regulars("tests/a.txt"), submodule("lib/tests"), submodule("tests/sub")), []string{"tests/sub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inventory.Suggest(entriesOf(tt.files...))
			if !reflect.DeepEqual(got.Submodules, tt.want) {
				t.Fatalf("Submodules = %#v, want %#v (suggestion %#v)", got.Submodules, tt.want, got)
			}
		})
	}
}

func TestSuggestLimitsTheSubmodulesToTwenty(t *testing.T) {
	files := regulars("a_test.go")
	var want []string
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("m%02d", i)
		files = append(files, submodule(name))
		if i < 20 {
			want = append(want, name)
		}
	}
	got := inventory.Suggest(entriesOf(files...))
	if !reflect.DeepEqual(got.Submodules, want) || got.Cut {
		t.Fatalf("Suggest = %#v, want the first 20 submodules and no cut", got)
	}
}
