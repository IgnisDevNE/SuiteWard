package scope_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// absent stands for a repository without .suiteward.yml in scopeOf.
const absent = ""

// scopeOf returns the scope of raw, or of NoDeclaration when raw is absent.
func scopeOf(t *testing.T, raw string) scope.Scope {
	t.Helper()
	if raw == absent {
		return scope.NewScope(scope.NoDeclaration())
	}
	d, err := scope.ParseDeclaration([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDeclaration(%q) error = %v, want nil", raw, err)
	}
	return scope.NewScope(d)
}

func TestScopeCovers(t *testing.T) {
	const declared = "version: 1\n" +
		"include:\n  - src/\n" +
		"exclude:\n  - src/gen/\n  - scripts/legacy.sh\n  - /.github/workflows/skip.yml\n" +
		"runner:\n  - scripts/\n  - Makefile\n"
	tests := []struct {
		name string
		raw  string
		path string
		want bool
	}{
		{"default declaration file without a declaration", absent, ".suiteward.yml", true},
		{"default workflow without a declaration", absent, ".github/workflows/ci.yml", true},
		{"default workflow nested beneath workflows", absent, ".github/workflows/sub/ci.yml", true},
		{"defaults are anchored to the root", absent, "sub/.github/workflows/ci.yml", false},
		{"nested declaration file is not a default", absent, "sub/.suiteward.yml", false},
		{"a file named workflows is not under the default directory", absent, ".github/workflows", false},
		{"default directory covers its direct child", absent, ".github/workflows/a", true},
		{"default directory is not a name prefix", absent, ".github/workflowsx/a", false},
		{"other file without a declaration", absent, "src/a.go", false},
		{"default declaration file with a declaration", declared, ".suiteward.yml", true},
		{"default workflow with a declaration", declared, ".github/workflows/ci.yml", true},
		{"include covers a file", declared, "src/a.go", true},
		{"runner covers a directory file", declared, "scripts/test.sh", true},
		{"runner covers a named file", declared, "Makefile", true},
		{"no rule covers the file", declared, "docs/a.md", false},
		{"exclude wins over include", declared, "src/gen/x.go", false},
		{"exclude wins over runner", declared, "scripts/legacy.sh", false},
		{"exclude wins over a default", declared, ".github/workflows/skip.yml", false},
		{"declaration file survives a matching wildcard exclude", "version: 1\nexclude: ['*.yml']\n", ".suiteward.yml", true},
		{"declaration file survives an exclude of everything", "version: 1\nexclude: ['**']\n", ".suiteward.yml", true},
		{"declaration file survives an exclude naming it", "version: 1\nexclude: [/.suiteward.yml]\n", ".suiteward.yml", true},
		{"wildcard exclude still removes a nested declaration file", "version: 1\ninclude: ['**']\nexclude: ['*.yml']\n", "sub/.suiteward.yml", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scopeOf(t, tt.raw).Covers(tt.path)
			if err != nil {
				t.Fatalf("Covers(%q) error = %v, want nil", tt.path, err)
			}
			if got != tt.want {
				t.Fatalf("Covers(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestScopeCoversRejectsInvalidPaths(t *testing.T) {
	s := scopeOf(t, "version: 1\ninclude: ['**']\n")
	for _, path := range []string{"", "/.suiteward.yml", "../x", "a//b", "a\\b", "./.suiteward.yml"} {
		t.Run(path, func(t *testing.T) {
			got, err := s.Covers(path)
			if !errors.Is(err, artifact.ErrInvalidPath) {
				t.Fatalf("Covers(%q) error = %v, want ErrInvalidPath", path, err)
			}
			if got {
				t.Fatalf("Covers(%q) = true alongside an error", path)
			}
		})
	}
}

func TestScopeKeepsItsDeclaration(t *testing.T) {
	d := scopeOf(t, goldenDeclaration).Declaration()
	if !d.Present() || !bytes.Equal(d.Raw(), []byte(goldenDeclaration)) {
		t.Fatalf("Declaration() = present %v raw %q, want the parsed declaration", d.Present(), d.Raw())
	}
	if scopeOf(t, absent).Declaration().Present() {
		t.Fatal("Declaration().Present() = true for NoDeclaration")
	}
}

func TestScopeDigest(t *testing.T) {
	// Golden values computed independently of the implementation from the
	// D-SCOPE v1 encoding described in the M1.3-A2 brief.
	golden := []struct {
		name string
		raw  string
		want string
	}{
		{"golden declaration", goldenDeclaration, "sha256:73faea98057e780271a99814faaa6bfda3fe4482c2ead9eac753dc7b129ad71e"},
		{"golden no declaration", absent, "sha256:5c18947ae886baaeee7dffc51a5f54928e2c7758510af00e21cee83d5b0b7ab9"},
	}
	for _, tt := range golden {
		t.Run(tt.name, func(t *testing.T) {
			s := scopeOf(t, tt.raw)
			if got := s.Digest().String(); got != tt.want {
				t.Fatalf("Digest() = %s, want %s", got, tt.want)
			}
			if again := scopeOf(t, tt.raw).Digest(); again != s.Digest() {
				t.Fatalf("Digest() is not stable: %s then %s", s.Digest(), again)
			}
		})
	}

	different := []struct {
		name        string
		left, right string
	}{
		{"one raw byte of whitespace", "version: 1\ninclude: [src/]\n", "version: 1\ninclude:  [src/]\n"},
		{"a trailing comment", "version: 1\ninclude: [src/]\n", "version: 1\ninclude: [src/]\n# note\n"},
		{"list order only", "version: 1\ninclude: [a, b]\n", "version: 1\ninclude: [b, a]\n"},
		{"a pattern moved between include and runner", "version: 1\ninclude: [a]\n", "version: 1\nrunner: [a]\n"},
		{"no declaration versus a declaration without lists", absent, "version: 1\n"},
	}
	for _, tt := range different {
		t.Run(tt.name, func(t *testing.T) {
			left, right := scopeOf(t, tt.left).Digest(), scopeOf(t, tt.right).Digest()
			if left.IsZero() || right.IsZero() {
				t.Fatalf("Digest() is absent: %v, %v", left, right)
			}
			if left == right {
				t.Fatalf("Digest() of %q and %q are both %s, want different", tt.left, tt.right, left)
			}
		})
	}
}

func TestReductions(t *testing.T) {
	tests := []struct {
		name                string
		governing, proposed string
		want                []string
	}{
		{"identical scopes", goldenDeclaration, goldenDeclaration, []string{}},
		{"both without a declaration", absent, absent, []string{}},
		{"removed include", "version: 1\ninclude: [src/, docs/]\n", "version: 1\ninclude: [src/]\n", []string{"rule docs/"}},
		{"removed runner", "version: 1\nrunner: [Makefile]\n", "version: 1\n", []string{"rule Makefile"}},
		{"added exclude", "version: 1\n", "version: 1\nexclude: [.github/workflows/ci.yml]\n", []string{"exclude .github/workflows/ci.yml"}},
		{"moved include to runner", "version: 1\ninclude: [Makefile]\n", "version: 1\nrunner: [Makefile]\n", []string{}},
		{"moved runner to include", "version: 1\nrunner: [Makefile]\n", "version: 1\ninclude: [Makefile]\n", []string{}},
		{"added include", "version: 1\n", "version: 1\ninclude: [src/]\n", []string{}},
		{"removed exclude", "version: 1\nexclude: [src/gen/]\n", "version: 1\n", []string{}},
		{"declaration deleted", "version: 1\ninclude: [src/]\nrunner: [Makefile]\n", absent, []string{"rule Makefile", "rule src/"}},
		{"a pattern in two lists is reported once", "version: 1\ninclude: [a]\nrunner: [a]\n", "version: 1\n", []string{"rule a"}},
		{
			"several reductions sorted",
			"version: 1\ninclude: [z, a]\nrunner: [m]\n",
			"version: 1\nexclude: [b]\n",
			[]string{"exclude b", "rule a", "rule m", "rule z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scope.Reductions(scopeOf(t, tt.governing), scopeOf(t, tt.proposed))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Reductions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestScopeMayCoverBelow(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		dir  string
		want bool
	}{
		{"default workflows directory below .github", absent, ".github", true},
		{"default workflows directory below workflows", absent, ".github/workflows", true},
		{"default workflows directory below a directory beneath it", absent, ".github/workflows/sub", true},
		{"no declaration below an unprotected directory", absent, "src", false},
		{"the declaration file default does not cover every directory", absent, "a", false},
		{"include below its directory", "version: 1\ninclude: [/src/**]\n", "src", true},
		{"include does not cover another directory", "version: 1\ninclude: [/src/**]\n", "lib", false},
		{"runner below its directory", "version: 1\nrunner: [/scripts/]\n", "scripts", true},
		{"exclude does not change the answer", "version: 1\ninclude: [/src/**]\nexclude: [/src/**]\n", "src", true},
		{"exclude alone covers nothing", "version: 1\nexclude: [/src/**]\n", "src", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scopeOf(t, tt.raw).MayCoverBelow(tt.dir)
			if err != nil {
				t.Fatalf("MayCoverBelow(%q) error = %v, want nil", tt.dir, err)
			}
			if got != tt.want {
				t.Errorf("MayCoverBelow(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}

func TestScopeMayCoverBelowRejectsInvalidDir(t *testing.T) {
	s := scopeOf(t, "version: 1\ninclude: ['**']\n")
	for _, dir := range []string{"", "/a", "a//b", "a\b", "../x"} {
		t.Run(dir, func(t *testing.T) {
			got, err := s.MayCoverBelow(dir)
			if !errors.Is(err, artifact.ErrInvalidPath) {
				t.Fatalf("MayCoverBelow(%q) error = %v, want ErrInvalidPath", dir, err)
			}
			if got {
				t.Fatalf("MayCoverBelow(%q) = true alongside an error", dir)
			}
		})
	}
}
