package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// chain returns n links l1..ln, each pointing at the next, the last at target.
func chain(n int, target string) []file {
	files := make([]file, 0, n)
	for i := 1; i <= n; i++ {
		next := fmt.Sprintf("l%d", i+1)
		if i == n {
			next = target
		}
		files = append(files, link(fmt.Sprintf("l%d", i), next))
	}
	return files
}

// TestBuildResolvesLinkTargets is D-SCOPE as amended 2026-10-17: a link to a regular file of the same tree behaves
// as a file; every other link keeps the old rule.
func TestBuildResolvesLinkTargets(t *testing.T) {
	unanchored := regular(declaration, "version: 1\ninclude: ['*_test.go']\n")
	base := []file{unanchored, regular("a/a_test.go", "a"), regular("CLAUDE.md", "c"), regular("docs/guide.md", "g")}
	with := func(extra ...file) []file { return slices.Concat(base, extra) }
	tests := []struct {
		name  string
		files []file
		safe  bool
	}{
		{"link to a file", with(link("AGENTS.md", "CLAUDE.md")), true},
		{"link to a file with a dot segment", with(link("AGENTS.md", "./CLAUDE.md")), true},
		{"relative to the link's directory", with(link("docs/AGENTS.md", "guide.md")), true},
		{"parent segment that stays inside", with(link("docs/AGENTS.md", "../CLAUDE.md")), true},
		{"chain of links to a file", with(link("AGENTS.md", "GEMINI.md"), link("GEMINI.md", "CLAUDE.md")), true},
		{"eight links are followed", with(chain(8, "CLAUDE.md")...), true},
		{"nine links are not", with(chain(9, "CLAUDE.md")...), false},
		{"absolute target", with(link("AGENTS.md", "/CLAUDE.md")), false},
		{"target escaping the root", with(link("AGENTS.md", "../CLAUDE.md")), false},
		{"target escaping through a directory", with(link("docs/AGENTS.md", "../../CLAUDE.md")), false},
		{"dangling target", with(link("AGENTS.md", "MISSING.md")), false},
		{"empty target", with(link("AGENTS.md", "")), false},
		{"target is the root", with(link("AGENTS.md", ".")), false},
		{"target with a trailing slash", with(link("AGENTS.md", "CLAUDE.md/")), false},
		{"loop", with(link("a", "b"), link("b", "a")), false},
		{"self loop", with(link("a", "a")), false},
		{"link to a directory", with(link("AGENTS.md", "docs")), false},
		{"link to a directory with a slash", with(link("AGENTS.md", "docs/")), false},
		{"link to a link to a directory", with(link("AGENTS.md", "x"), link("x", "docs")), false},
		{"link to a submodule", with(link("AGENTS.md", "vendor/lib"), submodule("vendor/lib")), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t, tc.files...)

			built, err := inventory.Build(context.Background(), tree)

			if !tc.safe {
				if !errors.Is(err, inventory.ErrUnsafeEntry) {
					t.Fatalf("Build error = %v, want ErrUnsafeEntry", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			// The link is not in the manifest: it is not covered, and its target is not either.
			requireManifest(t, built.Manifest(), tc.files, []string{declaration, "a/a_test.go"})
		})
	}
}

func TestBuildKeepsACoveredLinkUnsafeWhateverItPointsAt(t *testing.T) {
	for name, files := range map[string][]file{
		"covered by name":          {regular(declaration, "version: 1\ninclude: [AGENTS.md]\n"), regular("CLAUDE.md", "c"), link("AGENTS.md", "CLAUDE.md")},
		"covered by directory":     {regular(declaration, "version: 1\ninclude: [/tests/**]\n"), regular("CLAUDE.md", "c"), link("tests/AGENTS.md", "../CLAUDE.md")},
		"workflow link to a file":  {regular("CLAUDE.md", "c"), link(".github/workflows/ci.yml", "../../CLAUDE.md")},
		"covered link to its peer": {regular(declaration, "version: 1\ninclude: ['*.md']\n"), regular("CLAUDE.md", "c"), link("AGENTS.md", "CLAUDE.md")},
	} {
		t.Run(name, func(t *testing.T) {
			tree := newTree(t, files...)

			_, err := inventory.Build(context.Background(), tree)

			if !errors.Is(err, inventory.ErrUnsafeEntry) {
				t.Fatalf("Build error = %v, want ErrUnsafeEntry", err)
			}
		})
	}
}

func TestEvaluateResolvesLinkTargets(t *testing.T) {
	governing := scopeFrom(t, "version: 1\ninclude: ['*_test.go']\n")
	tree := newTree(t, regular("a_test.go", "a"), regular("CLAUDE.md", "c"), link("AGENTS.md", "CLAUDE.md"))

	manifest, err := inventory.Evaluate(context.Background(), tree, governing)

	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	requireManifest(t, manifest, tree.files, []string{"a_test.go"})
}

// TestBuildReadsALinkOnlyWhenItWouldBeUnsafe: the link blob is read for a link that a pattern may reach below and
// does not cover; any other link, and any submodule, costs no read.
func TestBuildReadsALinkOnlyWhenItWouldBeUnsafe(t *testing.T) {
	anchored := regular(declaration, "version: 1\ninclude: [/tests/**]\n")
	unanchored := regular(declaration, "version: 1\ninclude: ['*_test.go']\n")
	claude := regular("CLAUDE.md", "c")
	tests := []struct {
		name  string
		files []file
		reads []string
	}{
		{"link no pattern reaches", []file{anchored, claude, link("docs/link", "../CLAUDE.md")}, []string{declaration}},
		{"covered link", []file{anchored, claude, link("tests/link", "../CLAUDE.md")}, []string{declaration}},
		{"link that may be reached below", []file{unanchored, claude, link("AGENTS.md", "CLAUDE.md")}, []string{declaration, "AGENTS.md"}},
		{"chain reads each link once", []file{unanchored, claude, link("AGENTS.md", "B.md"), link("B.md", "CLAUDE.md")}, []string{declaration, "AGENTS.md", "B.md"}},
		{"submodule is never read", []file{unanchored, submodule("vendor/lib")}, []string{declaration}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t, tc.files...)

			_, _ = inventory.Build(context.Background(), tree) // a covered link fails; only the reads matter here

			if !slices.Equal(tree.reads, tc.reads) {
				t.Fatalf("reads = %v, want %v", tree.reads, tc.reads)
			}
		})
	}
}

func TestBuildFailsWhenALinkCannotBeRead(t *testing.T) {
	tree := newTree(t, regular(declaration, "version: 1\ninclude: ['*_test.go']\n"), regular("CLAUDE.md", "c"), link("AGENTS.md", "CLAUDE.md"))
	failure := errors.New("blob unavailable")
	tree.readErr = failure

	_, err := inventory.Build(context.Background(), tree)

	if !errors.Is(err, failure) || errors.Is(err, inventory.ErrUnsafeEntry) {
		t.Fatalf("Build error = %v, want the read failure and not ErrUnsafeEntry", err)
	}
}

// TestBuildLeavesSubmodulesOutOfScope: a submodule is never unsafe and never in the manifest.
func TestBuildLeavesSubmodulesOutOfScope(t *testing.T) {
	for name, text := range map[string]string{
		"unanchored include":       "version: 1\ninclude: ['*_test.go']\n",
		"anchored parent":          "version: 1\ninclude: [/vendor/**]\n",
		"the submodule's own path": "version: 1\ninclude: [/vendor/lib]\n",
		"runner":                   "version: 1\nrunner: [Makefile]\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := newTree(t, regular(declaration, text), regular("a_test.go", "a"), submodule("vendor/lib"))

			built, err := inventory.Build(context.Background(), tree)

			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if slices.Contains(paths(built.Manifest().Entries()), "vendor/lib") {
				t.Fatalf("manifest holds the submodule: %v", paths(built.Manifest().Entries()))
			}
		})
	}
}

// A suggested runner pattern may match no file (D-SCOPE-DISCOVERY); the integrity check builds on this inventory.
func TestBuildAcceptsARunnerPatternThatMatchesNoFile(t *testing.T) {
	tree := newTree(t, regular(declaration, "version: 1\nrunner: [pytest.toml, jest.config.*]\n"), regular("src/main.go", "m"))

	built, err := inventory.Build(context.Background(), tree)

	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	requireManifest(t, built.Manifest(), tree.files, []string{declaration})
}
