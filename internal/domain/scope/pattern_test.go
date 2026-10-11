package scope_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		// Rule 1: case-sensitive, "/" is the only separator.
		{"literal match", "docs/readme.md", "docs/readme.md", true},
		{"case mismatch does not match", "docs/readme.md", "Docs/Readme.md", false},

		// Rule 2: anchored (leading "/" or an interior "/") versus unanchored.
		{"anchored matches only at root", "/vendor", "vendor/a.go", true},
		{"anchored does not match nested occurrence", "/vendor", "pkg/vendor/a.go", false},
		{"unanchored matches at any depth", "vendor", "pkg/vendor/a.go", true},
		{"interior slash before last char anchors the pattern", "a/b", "x/a/b", false},

		// Rule 3: trailing "/" names a directory, never a file of that name.
		{"directory pattern matches beneath the directory", "docs/", "docs/a.md", true},
		{"directory pattern does not match a file of the same name", "docs/", "docs", false},
		{"pattern without trailing slash matches the file itself", "docs", "docs", true},

		// Rule 4: a pattern that matches a directory matches every file beneath it.
		{"unanchored directory name covers direct child", "docs", "docs/a.md", true},
		{"unanchored directory name covers deeply nested child", "docs", "x/docs/b/c.md", true},

		// Rule 5: "**" as a whole segment matches zero or more segments;
		// "*", "?" and "[...]" within a segment follow path.Match and never cross "/".
		{"leading **/ matches at the root", "**/fixtures", "fixtures/a.go", true},
		{"leading **/ matches nested", "**/fixtures", "x/y/fixtures/a.go", true},
		{"trailing /** matches zero segments beneath", "tests/**", "tests/a.go", true},
		{"trailing /** matches many segments beneath", "tests/**", "tests/a/b/c.go", true},
		{"trailing /** requires the tests prefix", "tests/**", "other/a.go", false},
		{"a/**/b matches zero segments between", "a/**/b", "a/b", true},
		{"a/**/b matches several segments between", "a/**/b", "a/x/y/b", true},
		{"segment wildcard matches within a segment", "*.go", "a.go", true},
		{"segment wildcard does not cross a directory boundary", "dir*/file.go", "dirZZZ/file.go", true},
		{"segment wildcard does not match a differently shaped path", "dir*/file.go", "dirZZZ_file.go", false},
		{"segment question mark matches one rune", "fixture?.go", "fixture1.go", true},
		{"segment question mark rejects extra runes", "fixture?.go", "fixture12.go", false},
		{"segment class matches a listed rune", "file[12].go", "file1.go", true},
		{"segment class rejects an unlisted rune", "file[12].go", "file3.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern, err := scope.ParsePattern(tt.pattern)
			if err != nil {
				t.Fatalf("ParsePattern(%q) error = %v, want nil", tt.pattern, err)
			}
			got, err := pattern.Match(tt.path)
			if err != nil {
				t.Fatalf("Match(%q) error = %v, want nil", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("Pattern(%q).Match(%q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestParsePatternRejectsInvalidForms(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"negated", "!docs"},
		{"backslash", `docs\a.go`},
		{"slash alone", "/"},
		{"empty segment", "a//b"},
		{"dot segment", "./a"},
		{"dot segment middle", "a/./b"},
		{"dotdot segment", "a/../b"},
		{"dotdot segment alone", ".."},
		{"double star mixed prefix", "a**"},
		{"double star mixed suffix", "**.go"},
		{"malformed class", "a[b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := scope.ParsePattern(tt.pattern); !errors.Is(err, scope.ErrInvalidPattern) {
				t.Fatalf("ParsePattern(%q) error = %v, want ErrInvalidPattern", tt.pattern, err)
			}
		})
	}
}

func TestPatternMatchRejectsInvalidPath(t *testing.T) {
	pattern, err := scope.ParsePattern("docs/**")
	if err != nil {
		t.Fatalf("ParsePattern() error = %v, want nil", err)
	}
	tests := []struct {
		name string
		path string
	}{
		{"empty segment", "docs//a.md"},
		{"dot segment", "docs/./a.md"},
		{"backslash", `docs\a.md`},
		{"nul byte", "docs/a\x00.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := pattern.Match(tt.path); !errors.Is(err, artifact.ErrInvalidPath) {
				t.Fatalf("Match(%q) error = %v, want ErrInvalidPath", tt.path, err)
			}
		})
	}
}

func TestPatternStringReturnsSourceText(t *testing.T) {
	tests := []string{
		"docs",
		"docs/",
		"/docs",
		"**/fixtures",
		"tests/**",
		"a/**/b",
		"*.go",
	}
	for _, source := range tests {
		t.Run(source, func(t *testing.T) {
			pattern, err := scope.ParsePattern(source)
			if err != nil {
				t.Fatalf("ParsePattern(%q) error = %v, want nil", source, err)
			}
			if got := pattern.String(); got != source {
				t.Errorf("String() = %q, want %q", got, source)
			}
		})
	}
}
