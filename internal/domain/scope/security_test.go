package scope_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// An alias lets one node stand for another. Each alias here points at a node that would be valid in its position, so
// the cases fail only if aliases are refused: the node-kind checks of ParseDeclaration refuse them, not a dedicated ban.
func TestParseDeclarationRejectsAliasesInEveryPosition(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"alias as the version value", "include: [&v 1]\nversion: *v\n"},
		{"alias as the include list", "exclude: &l [a]\nversion: 1\ninclude: *l\n"},
		{"alias as an include item", "exclude: [&p a/]\nversion: 1\ninclude: [*p]\n"},
		{"alias as the exclude list", "include: &l [a]\nversion: 1\nexclude: *l\n"},
		{"alias as an exclude item", "include: [&p a/]\nversion: 1\nexclude: [*p]\n"},
		{"alias as a runner item", "include: [&p a]\nversion: 1\nrunner: [*p]\n"},
		{"alias as a key", "&k include: [a]\nversion: 1\n*k : [b]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := scope.ParseDeclaration([]byte(tt.raw)); !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want ErrInvalidDeclaration", tt.raw, err)
			}
		})
	}
}

// An anchor that no alias uses changes nothing the declaration says, and ParseDeclaration accepts it as it stands.
func TestParseDeclarationAcceptsAnAnchorNoAliasUses(t *testing.T) {
	d, err := scope.ParseDeclaration([]byte("version: &v 1\ninclude: [&p a]\n"))
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	if got := d.Include(); len(got) != 1 || got[0].String() != "a" {
		t.Fatalf("include = %v, want [a]", got)
	}
}

func TestParseDeclarationRejectsDeepNestingWithoutPanicking(t *testing.T) {
	for _, tt := range []struct{ name, open, close string }{
		{"sequences", "[", "]"},
		{"mappings", "{a: ", "}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := "version: 1\ninclude: " + strings.Repeat(tt.open, 10000) + strings.Repeat(tt.close, 10000) + "\n"
			if _, err := scope.ParseDeclaration([]byte(raw)); !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration of %d nested values error = %v, want ErrInvalidDeclaration", 10000, err)
			}
		})
	}
}

// No size cap exists: a declaration of 1 MiB is parsed like any other, and what it holds decides. The test pins that,
// and that the parse stays far below the bound.
func TestParseDeclarationOfOneMebibyte(t *testing.T) {
	const size = 1 << 20
	var many strings.Builder
	many.WriteString("version: 1\ninclude:\n")
	for i := 0; many.Len() < size; i++ {
		many.WriteString("  - d" + strconv.Itoa(i) + "/\n")
	}
	for name, raw := range map[string]string{
		"thousands of distinct patterns": many.String(),
		"one pattern of a megabyte":      "version: 1\ninclude:\n  - " + strings.Repeat("a", size) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			if _, err := scope.ParseDeclaration([]byte(raw)); err != nil {
				t.Fatalf("ParseDeclaration of %d bytes: %v", len(raw), err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("ParseDeclaration of %d bytes took %v", len(raw), elapsed)
			}
		})
	}
}

// Patterns and paths match byte for byte: a look-alike character is another character.
func TestPatternDoesNotMatchLookAlikeDirectories(t *testing.T) {
	pattern, err := scope.ParsePattern("tests/**")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, path string
		want       bool
	}{
		{"the directory itself", "tests/a_test.go", true},
		{"Cyrillic e in the directory", "t\u0435sts/a_test.go", false},
		{"fullwidth s in the directory", "te\uff53ts/a_test.go", false},
		{"fullwidth solidus is no separator", "tests\uff0fa_test.go", false},
		{"decomposed accent next to the name", "tests\u0301/a_test.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pattern.Match(tt.path)
			if err != nil || got != tt.want {
				t.Fatalf("Match(%q) = %v, %v, want %v", tt.path, got, err, tt.want)
			}
		})
	}
}

// The scope is case sensitive, like the git tree it covers: Tests/ is another directory than tests/, so a scope that
// protects tests/ leaves Tests/ unprotected. On a case-insensitive checkout the two are one directory.
func TestScopeTreatsDirectoriesThatDifferInCaseAsDifferentDirectories(t *testing.T) {
	declaration, err := scope.ParseDeclaration([]byte("version: 1\ninclude: [tests/]\n"))
	if err != nil {
		t.Fatal(err)
	}
	effective := scope.NewScope(declaration)
	for path, want := range map[string]bool{"tests/a_test.go": true, "Tests/a_test.go": false} {
		if got, err := effective.Covers(path); err != nil || got != want {
			t.Errorf("Covers(%q) = %v, %v, want %v", path, got, err, want)
		}
	}
}
