package inventory_test

import (
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// UncoveredRunners is Uncovered for the runner families only (D-SCOPE-DISCOVERY, "Uncovered runner files block"): a test file is
// never a runner file, though Suggest gives a Python test file the pytest group.
func TestUncoveredRunnersKeepsOnlyRunnerNamedFilesTheScopeLeavesOut(t *testing.T) {
	tests := []struct {
		name        string
		declaration string
		files       []file
		want        []string
	}{
		{"empty input", "", nil, none},
		{"runner files only, sorted, test files left out", "", regulars("z/jest.config.ts", "conftest.py", "tests/test_a.py", "a_test.go", "src/main.go", "package.json", "a/.mocharc.json"),
			[]string{"a/.mocharc.json", "conftest.py", "z/jest.config.ts"}},
		{"a Python test file is not a runner file", "", regulars("test_x.py", "pkg/x_test.py", "tests.py"), none},
		{"a covered runner file is left out", "version: 1\nrunner: [jest.config.*]\n", regulars("jest.config.js", "conftest.py"), []string{"conftest.py"}},
		{"an excluded runner file is left out", "version: 1\nexclude: [legacy/, conftest.py]\n", regulars("legacy/jest.config.js", "conftest.py", "pytest.ini"), []string{"pytest.ini"}},
		{"a vendored runner file is left out", "", regulars("node_modules/x/jest.config.js", ".venv/conftest.py", "vendor/phpunit.xml"), none},
		{"a link or a submodule of a runner name is left out", "", append(regulars("pytest.ini"), symlink("jest.config.js"), submodule("karma.conf.js")), []string{"pytest.ini"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inventory.UncoveredRunners(entriesOf(tt.files...), scopeFrom(t, tt.declaration))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("UncoveredRunners = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestUncoveredRunnersRejectsAnInvalidPath(t *testing.T) {
	if _, err := inventory.UncoveredRunners(entriesOf(regular("../conftest.py", "")), scopeFrom(t, "")); err == nil {
		t.Fatal("UncoveredRunners accepted an invalid path")
	}
}

// RunnerPatterns is what protecting runner files adds to the runner list: the whole group of each family found, sorted and without repeats.
func TestRunnerPatternsAreTheGroupsOfTheRunnerFamiliesFound(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"none", nil, none},
		{"one family", []string{"jest.config.js"}, []string{"jest.config.*"}},
		{"a group", []string{"a/conftest.py"}, []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}},
		{"two files of one family and one of another", []string{"jest.config.js", "b/jest.config.ts", ".coveragerc"}, []string{".c8rc*", ".coveragerc", ".nycrc*", "jest.config.*"}},
		{"a test file brings nothing", []string{"test_x.py", "a_test.go"}, none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inventory.RunnerPatterns(tt.paths); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RunnerPatterns = %v, want %v", got, tt.want)
			}
		})
	}
}
