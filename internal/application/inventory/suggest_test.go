package inventory_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

func entriesOf(files ...file) []inventory.Entry {
	entries := make([]inventory.Entry, 0, len(files))
	for _, f := range files {
		entries = append(entries, inventory.Entry{Path: f.path, Kind: f.kind})
	}
	return entries
}

func TestSuggestScope(t *testing.T) {
	tests := []struct {
		name  string
		files []file
		want  []string
	}{
		{"empty input", nil, []string{}},
		{"no test-like path", []file{regular("main.go", ""), regular("internal/app/app.go", "")}, []string{}},
		{"tests directory", []file{regular("tests/a/b.txt", "")}, []string{"tests/**"}},
		{"test directory", []file{regular("test/a.txt", "")}, []string{"test/**"}},
		{"dunder tests directory", []file{regular("__tests__/a.js", "")}, []string{"__tests__/**"}},
		{"spec directory", []file{regular("spec/a.rb", "")}, []string{"spec/**"}},
		{"nested tests directory is not top level", []file{regular("pkg/tests/a.txt", "")}, []string{}},
		{"underscore test file", []file{regular("internal/app/app_test.go", "")}, []string{"internal/app/*_test.*"}},
		{"dot test file", []file{regular("web/a.test.ts", "")}, []string{"web/*.test.*"}},
		{"dot spec file", []file{regular("web/a.spec.ts", "")}, []string{"web/*.spec.*"}},
		{"python test file", []file{regular("pkg/test_a.py", "")}, []string{"pkg/test_*.py"}},
		{"test_ prefix on a non-python file is not a test", []file{regular("pkg/test_a.go", "")}, []string{}},
		{"root file", []file{regular("a_test.go", "")}, []string{"*_test.*"}},
		{"file matching two globs", []file{regular("pkg/a_test.spec.go", "")}, []string{"pkg/*.spec.*", "pkg/*_test.*"}},
		{"sorted and de-duplicated", []file{
			regular("z/b_test.go", ""), regular("z/a_test.go", ""), regular("tests/x", ""), regular("tests/y", ""), regular("a/c_test.go", ""),
		}, []string{"a/*_test.*", "tests/**", "z/*_test.*"}},
		{"directories, symlinks and submodules are ignored", []file{
			symlink("tests/link"), submodule("spec/sub"), symlink("pkg/a_test.go"), submodule("pkg/b.spec.go"),
		}, []string{}},
		{"a directory with glob characters is skipped", []file{regular("a[b]/c_test.go", ""), regular("a*/c_test.go", ""), regular("a?/c_test.go", "")}, []string{}},
		{"a top-level directory name alone is not a file inside it", []file{regular("tests", "")}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inventory.SuggestScope(entriesOf(tt.files...)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SuggestScope = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSuggestScopeCapKeepsTheFirstTwentySorted(t *testing.T) {
	var files []file
	var want []string
	for i := 0; i < 30; i++ {
		files = append(files, regular(fmt.Sprintf("d%02d/a_test.go", i), ""))
	}
	for i := 0; i < 20; i++ {
		want = append(want, fmt.Sprintf("d%02d/*_test.*", i))
	}
	// The input order must not decide which patterns survive.
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}
	if got := inventory.SuggestScope(entriesOf(files...)); !reflect.DeepEqual(got, want) {
		t.Fatalf("SuggestScope = %v, want %v", got, want)
	}
}

func TestSuggestScopePatternsParseAndMatchTheirSource(t *testing.T) {
	sources := []file{
		regular("tests/a/b.txt", ""), regular("test/a.txt", ""), regular("__tests__/a.js", ""), regular("spec/a.rb", ""),
		regular("internal/app/app_test.go", ""), regular("web/a.test.ts", ""), regular("web/a.spec.ts", ""),
		regular("pkg/test_a.py", ""), regular("a_test.go", ""), regular("b.spec.js", ""), regular("deep/er/x.test.go", ""),
	}
	patterns := inventory.SuggestScope(entriesOf(sources...))
	parsed := make([]scope.Pattern, 0, len(patterns))
	for _, text := range patterns {
		p, err := scope.ParsePattern(text)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", text, err)
		}
		parsed = append(parsed, p)
	}
	for _, source := range sources {
		matched := false
		for _, p := range parsed {
			ok, err := p.Match(source.path)
			if err != nil {
				t.Fatalf("Match(%q, %q): %v", p, source.path, err)
			}
			matched = matched || ok
		}
		if !matched {
			t.Errorf("no suggested pattern of %v matches %q", patterns, source.path)
		}
	}
}
