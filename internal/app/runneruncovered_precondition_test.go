package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The two declarations of a runner-uncovered comment are built from a canonical that governance parsed and verified against its approved scope, the
// constant runner patterns, and exclude patterns of paths that excludable approved: no head input can make scope.AddPatterns or scope.AddExcludes
// fail, so explainRunners returns the failure as an error instead of skipping the comment (decision 5 of M1.10).

// canonicalShapes are canonical declarations of different shapes: line endings, styles, comments, quoting and lists that are missing or empty.
var canonicalShapes = map[string]string{
	"block":           "version: 1\ninclude:\n  - /tests/**\n",
	"flow":            "version: 1\ninclude: [/tests/**]\n",
	"crlf":            "version: 1\r\ninclude:\r\n  - /tests/**\r\nrunner:\r\n  - conftest.py\r\n",
	"comments":        "# top\nversion: 1 # the schema\ninclude:\n  # tests\n  - /tests/** # all\nrunner: # the runners\n  - jest.config.*\n# end\n",
	"empty runner":    "version: 1\ninclude: [/tests/**]\nrunner: []\n",
	"flow runner":     "version: 1\ninclude: [/tests/**]\nrunner: [conftest.py, 'pytest.ini', \".nycrc*\"]\n",
	"double quoted":   "version: 1\ninclude:\n  - \"/tests/**\"\nrunner:\n  - \"conftest.py\"\n",
	"single quoted":   "version: 1\ninclude:\n  - '/tests/**'\nexclude:\n  - 'vendor/'\n",
	"mixed quoting":   "version: 1\ninclude:\n  - /tests/**\n  - \"/e2e/**\"\n  - '/spec/**'\n",
	"with exclude":    "version: 1\ninclude: [/tests/**]\nexclude: [/tests/fixtures/**, build/]\n",
	"every runner":    "version: 1\ninclude: [/tests/**]\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini, conftest.py, 'jest.config.*', 'vitest.config.*', 'vitest.workspace.*', '.mocharc.*', 'karma.conf.*']\n",
	"an anchor":       "version: 1\ninclude: &tests [/tests/**]\n",
	"no trailing eol": "version: 1\ninclude: [/tests/**]",
	"document marker": "---\nversion: 1\ninclude: [/tests/**]\n",
	"long list": "version: 1\ninclude:\n" + strings.Repeat("  - /dir/**\n", 1) + func() string {
		var b strings.Builder
		for i := range 300 {
			b.WriteString("  - /d" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + "/**\n")
		}
		return b.String()
	}(),
}

// everyRunnerName is a file of each runner family of the inventory, so the patterns of all of them are built.
var everyRunnerName = []string{"conftest.py", "jest.config.js", "vitest.config.ts", ".mocharc.json", "karma.conf.js", "playwright.config.ts", "cypress.config.js", ".coveragerc",
	"phpunit.xml", ".rspec", "test_helper.rb", "dart_test.yaml", "codecov.yml"}

func TestTheRunnerPatternsAlwaysGoIntoACanonicalDeclaration(t *testing.T) {
	all := inventory.RunnerPatterns(everyRunnerName)
	if len(all) < 25 { // 25 is the number of patterns of the runner groups of the inventory today; a group added later needs a name in everyRunnerName
		t.Fatalf("%d runner patterns for every family, want every pattern of the inventory", len(all))
	}
	for name, base := range canonicalShapes {
		if _, err := scope.ParseDeclaration([]byte(base)); err != nil {
			t.Fatalf("%s: the canonical shape is not a declaration: %v", name, err)
		}
		t.Run(name, func(t *testing.T) {
			for _, paths := range append([][]string{everyRunnerName}, singles(everyRunnerName)...) {
				if _, err := scope.AddPatterns([]byte(base), nil, inventory.RunnerPatterns(paths)); err != nil {
					t.Fatalf("AddPatterns for %v: %v", paths, err)
				}
			}
		})
	}
}

// adversarialPaths are paths that a head can hold and excludable accepts: YAML syntax, scalars YAML would read as another type, a leading
// space or dash, Unicode and a long name.
var adversarialPaths = []string{
	"#hash/conftest.py", "a: b/jest.config.js", "\"quoted\"/conftest.py", "'single'/conftest.py", " lead/conftest.py", "trail /conftest.py", "- dash/conftest.py",
	"ünï/コード/conftest.py", strings.Repeat("l", 200) + "/conftest.py", "a b/c d/jest.config.js", "key: value", "true/conftest.py", "null/conftest.py", "1e3/conftest.py",
	"{brace}/conftest.py", "@at/conftest.py", "%pct/conftest.py", "&amp/conftest.py", "|pipe/conftest.py", ">gt/conftest.py", "~tilde/conftest.py", "tab\there/conftest.py",
	"a\u200bb/conftest.py", "line\nbreak/conftest.py", "back`tick/conftest.py", "conftest.py", "jest.config.js", "-", "---", "...", "yes", "0x1f/jest.config.js", "#",
}

func TestTheIgnorePatternsOfAnExcludablePathAlwaysGoIntoACanonicalDeclaration(t *testing.T) {
	for name, base := range canonicalShapes {
		t.Run(name, func(t *testing.T) {
			for _, path := range adversarialPaths {
				paths := []string{path}
				if !excludable(paths) {
					continue
				}
				if _, err := scope.AddExcludes([]byte(base), excludePatterns(paths)); err != nil {
					t.Fatalf("AddExcludes for %q: %v", path, err)
				}
			}
			var all []string
			for _, path := range adversarialPaths {
				if excludable([]string{path}) {
					all = append(all, path)
				}
			}
			if _, err := scope.AddExcludes([]byte(base), excludePatterns(all)); err != nil {
				t.Fatalf("AddExcludes for every path: %v", err)
			}
		})
	}
}

func singles(names []string) [][]string {
	out := make([][]string, len(names))
	for i, name := range names {
		out[i] = []string{"pkg/" + name}
	}
	return slices.Clip(out)
}

// A path with invalid UTF-8 never reaches AddExcludes: the inventory refuses it as an invalid tree (artifact.ValidPath) before runnerFiles builds any pattern from it.
func TestAPathWithInvalidUTF8NeverReachesTheExcludes(t *testing.T) {
	tree := &bootTree{files: map[string]string{".suiteward.yml": cjDecl, "a\xffb/conftest.py": "x\n"}}

	if _, err := inventory.ListEntries(t.Context(), tree); !errors.Is(err, inventory.ErrInvalidTree) {
		t.Fatalf("ListEntries error = %v, want ErrInvalidTree", err)
	}
}
