package inventory_test

import (
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// Test files of 27 include patterns and one runner file: a runner pattern the base lacks is not pushed out by the include patterns.
func TestSuggestAgainstKeepsTheMissingRunnerPatternInsideTheLimit(t *testing.T) {
	entries := entriesOf(append(allIncludeSources(), regulars("jest.config.js")...)...)
	got, err := inventory.SuggestAgainst(entries, scopeOf(t, "version: 1\n"))
	if err != nil || !slices.Equal(got.Runner, []string{"jest.config.*"}) || len(got.Include) != 19 || !got.Cut {
		t.Fatalf("SuggestAgainst = %+v, %v; want the runner pattern, 19 include patterns (20 in all) and a cut", got, err)
	}
}

// MissingRunner reads the runner patterns before any limit: 27 include patterns used to hide the missing runner pattern.
func TestMissingRunnerSeesARunnerPatternWhateverTheIncludeCount(t *testing.T) {
	entries := entriesOf(append(allIncludeSources(), regulars("jest.config.js")...)...)
	got, err := inventory.MissingRunner(entries, scopeOf(t, "version: 1\n"))
	if err != nil || !slices.Equal(got, []string{"jest.config.*"}) {
		t.Fatalf("MissingRunner = %v, %v; want jest.config.*", got, err)
	}
}

// All 13 runner families bring 25 patterns. MissingRunner reports every one; a suggestion holds 20 in all, with a cut.
func TestRunnerPatternsAloneMayExceedTheLimit(t *testing.T) {
	entries := entriesOf(regulars("conftest.py", "jest.config.js", "vitest.config.ts", ".mocharc.json", "karma.conf.js", "playwright.config.ts", "cypress.config.ts",
		".coveragerc", "phpunit.xml", "spec_helper.rb", "test_helper.exs", "dart_test.yaml", "codecov.yml", "a_test.go")...)
	base := scopeOf(t, "version: 1\n")
	missing, err := inventory.MissingRunner(entries, base)
	if err != nil || len(missing) != 25 {
		t.Fatalf("MissingRunner = %d patterns, %v; want 25", len(missing), err)
	}
	got, err := inventory.SuggestAgainst(entries, base)
	if err != nil || len(got.Runner) != 20 || len(got.Include) != 0 || !got.Cut {
		t.Fatalf("SuggestAgainst = %d include, %d runner, cut %v, %v; want 20 runner patterns in all and a cut", len(got.Include), len(got.Runner), got.Cut, err)
	}
}
