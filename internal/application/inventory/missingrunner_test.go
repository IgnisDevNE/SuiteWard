package inventory_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

func TestMissingRunnerNeedsNoFileOfThatName(t *testing.T) {
	pytest := entriesOf(regulars("app/test_x.py")...)
	for name, tc := range map[string]struct {
		entries     []inventory.Entry
		declaration string
		want        []string
	}{
		"nothing listed":             {pytest, "version: 1\ninclude: [\"test_*.py\"]\n", []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}},
		"a listed name":              {pytest, "version: 1\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini]\n", []string{"conftest.py"}},
		"a covering glob":            {pytest, "version: 1\nrunner: [pytest.*, .pytest.*, conftest.py]\n", none},
		"an excluded name":           {pytest, "version: 1\nrunner: [pytest.*, .pytest.*]\nexclude: [conftest.py]\n", none},
		"a listed glob":              {entriesOf(regulars("jest.config.ts")...), "version: 1\nrunner: [jest.config.*]\n", none},
		"a glob only another covers": {entriesOf(regulars("jest.config.ts")...), "version: 1\nrunner: [jest.*]\n", []string{"jest.config.*"}},
		"an excluded glob":           {entriesOf(regulars("jest.config.ts")...), "version: 1\nexclude: [jest.config.*]\n", none},
		"no runner file":             {entriesOf(regulars("src/main.go")...), "version: 1\n", none},
	} {
		got, err := inventory.MissingRunner(tc.entries, scopeOf(t, tc.declaration))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: MissingRunner = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}

func TestWithRunnerAddsSortedWithoutRepeatsAndKeepsTheLimit(t *testing.T) {
	got := inventory.Suggestion{Include: []string{"tests/"}, Runner: []string{"b"}}.WithRunner([]string{"c", "a", "b"})
	if want := []string{"a", "b", "c"}; !slices.Equal(got.Runner, want) || got.Cut {
		t.Errorf("runner %v cut %v; want %v uncut", got.Runner, got.Cut, want)
	}
	include := make([]string, 18)
	for i := range include {
		include[i] = fmt.Sprintf("d%02d/", i)
	}
	got = inventory.Suggestion{Include: include}.WithRunner([]string{"x", "y", "z"})
	if want := []string{"x", "y"}; !slices.Equal(got.Runner, want) || !got.Cut {
		t.Errorf("runner %v cut %v; want %v cut at 20 patterns in all", got.Runner, got.Cut, want)
	}
}

// SuggestAgainst is Suggest without the runner patterns the base handles (see MissingRunner for the rule), dropped before the limit applies.
func TestSuggestAgainstLeavesOutTheRunnerPatternsTheBaseHandles(t *testing.T) {
	pytest := entriesOf(regulars("app/test_x.py")...)
	jest := entriesOf(regulars("jest.config.ts")...)
	pytestFamily := []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}
	for name, tc := range map[string]struct {
		entries     []inventory.Entry
		declaration string
		handled     []string // the names of Suggest(entries).Runner the base handles
	}{
		"nothing listed":             {pytest, "version: 1\ninclude: [\"test_*.py\"]\n", nil},
		"a listed name":              {pytest, "version: 1\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini]\n", []string{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini"}},
		"names a glob covers":        {pytest, "version: 1\nrunner: [pytest.*, .pytest.*]\n", []string{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini"}},
		"names an include covers":    {pytest, "version: 1\ninclude: [pytest.*, .pytest.*, conftest.py]\n", pytestFamily},
		"an excluded name":           {pytest, "version: 1\nexclude: [conftest.py]\n", []string{"conftest.py"}},
		"a listed glob":              {jest, "version: 1\nrunner: [jest.config.*]\n", []string{"jest.config.*"}},
		"a glob only another covers": {jest, "version: 1\nrunner: [jest.*]\n", nil},
		"an excluded glob":           {jest, "version: 1\nexclude: [jest.config.*]\n", []string{"jest.config.*"}},
	} {
		unfiltered := inventory.Suggest(tc.entries)
		want := slices.DeleteFunc(slices.Clone(unfiltered.Runner), func(p string) bool { return slices.Contains(tc.handled, p) })
		got, err := inventory.SuggestAgainst(tc.entries, scopeOf(t, tc.declaration))
		if err != nil || !slices.Equal(got.Runner, want) {
			t.Errorf("%s: Runner = %v, %v; want %v", name, got.Runner, err, want)
		}
		if !slices.Equal(got.Include, unfiltered.Include) || !slices.Equal(got.Mixed, unfiltered.Mixed) || !slices.Equal(got.Submodules, unfiltered.Submodules) || got.Cut != unfiltered.Cut {
			t.Errorf("%s: %+v; want the include, mixed, submodules and cut of %+v", name, got, unfiltered)
		}
	}
}

// 16 test files bring 16 include patterns and the 5 pytest names: 21 patterns, one over the limit. Names the base handles free the room.
func TestSuggestAgainstCutsOnlyWhatIsStillMissing(t *testing.T) {
	entries := entriesOf(regulars("a_test.go", "a.test.js", "a.spec.js", "a.cy.js", "a_spec.rb", "a_test.rb", "a_test.exs", "a_test.dart",
		"ATest.java", "ATests.java", "ATest.kt", "ATests.cs", "ATest.php", "test_a.py", "a_test.py", "tests.py")...)
	for name, tc := range map[string]struct {
		declaration string
		runner      []string
		cut         bool
	}{
		"nothing handled":   {"version: 1\n", []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini"}, true},
		"one name handled":  {"version: 1\nrunner: [pytest.toml]\n", []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini"}, false},
		"the family listed": {"version: 1\nrunner: [pytest.toml, .pytest.toml, pytest.ini, .pytest.ini, conftest.py]\n", none, false},
	} {
		got, err := inventory.SuggestAgainst(entries, scopeOf(t, tc.declaration))
		if err != nil || got.Cut != tc.cut || !slices.Equal(got.Runner, tc.runner) || len(got.Include) != 16 {
			t.Errorf("%s: runner %v, cut %v, %d includes, %v; want runner %v, cut %v, 16 includes", name, got.Runner, got.Cut, len(got.Include), err, tc.runner, tc.cut)
		}
	}
}
