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
