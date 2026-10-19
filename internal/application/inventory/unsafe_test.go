package inventory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// An unsafe entry names its path, so the caller can tell the owner which link or file is the problem (M1.9-E9).
func TestAnUnsafeEntryNamesItsPath(t *testing.T) {
	tests := []struct {
		name  string
		files []file
		path  string
	}{
		{"a link the scope covers", []file{regular(declaration, "version: 1\ninclude: [/tests/**]\n"), symlink("tests/link")}, "tests/link"},
		{"a link to a directory that a pattern reaches below it",
			[]file{regular(declaration, "version: 1\ninclude: ['**/test_*.py']\n"), link("spec_link", "app"), regular("app/main.py", "m")}, "spec_link"},
		{"the declaration as a link", []file{symlink(declaration)}, declaration},
		{"the declaration as a submodule", []file{submodule(declaration)}, declaration},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inventory.Build(context.Background(), newTree(t, tc.files...))

			var unsafe *inventory.UnsafeEntryError
			if !errors.Is(err, inventory.ErrUnsafeEntry) || !errors.As(err, &unsafe) || unsafe.Path != tc.path {
				t.Fatalf("Build = %v, want ErrUnsafeEntry and an UnsafeEntryError for %q", err, tc.path)
			}
		})
	}
}

func TestEvaluateNamesTheUnsafeEntryUnderTheGoverningScope(t *testing.T) {
	parsed, err := scope.ParseDeclaration([]byte("version: 1\ninclude: [/tests/**]\n"))
	if err != nil {
		t.Fatal(err)
	}
	governing := scope.NewScope(parsed)

	_, err = inventory.Evaluate(context.Background(), newTree(t, symlink("tests/link")), governing)

	var unsafe *inventory.UnsafeEntryError
	if !errors.Is(err, inventory.ErrUnsafeEntry) || !errors.As(err, &unsafe) || unsafe.Path != "tests/link" {
		t.Fatalf("Evaluate = %v, want ErrUnsafeEntry and an UnsafeEntryError for tests/link", err)
	}
}

// The text is what the error said before it was typed: logs and tests that quote it keep working.
func TestAnUnsafeEntryErrorKeepsItsText(t *testing.T) {
	_, err := inventory.Build(context.Background(), newTree(t, regular(declaration, "version: 1\ninclude: [/tests/**]\n"), symlink("tests/link")))

	if want := `unsafe entry in the protected scope: "tests/link"`; err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
}

// D-SCOPE, an accepted link: an exclude that names exactly the link's path skips the link, so it is neither read nor unsafe nor
// covered, and nothing below it is protected. Any broader exclude keeps the rule.
func TestBuildAcceptsALinkThatAnExactExcludeNames(t *testing.T) {
	tests := []struct {
		name    string
		link    string
		exclude string
		accept  bool
	}{
		{"anchored", "spec_link", "/spec_link", true},
		{"nested, interior slash", "a/l", "a/l", true},
		{"nested, anchored", "a/l", "/a/l", true},
		{"unanchored name", "spec_link", "spec_link", false},
		{"glob", "spec_link", "spec_*", false},
		{"below the link", "spec_link", "/spec_link/**", false},
		{"a directory pattern", "spec_link", "/spec_link/", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newTree(t,
				regular(declaration, "version: 1\ninclude: ['**/test_*.py', '/spec_link/**']\nexclude: ['"+tc.exclude+"']\n"),
				link(tc.link, "app"), regular("app/test_main.py", "t"), regular("app/main.py", "m"), regular("a/test_x.py", "x"))

			built, err := inventory.Build(context.Background(), tree)

			if !tc.accept {
				var unsafe *inventory.UnsafeEntryError
				if !errors.As(err, &unsafe) || unsafe.Path != tc.link {
					t.Fatalf("Build = %v, want an UnsafeEntryError for %q", err, tc.link)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build error = %v, want nil: the exclude accepts the link", err)
			}
			for _, entry := range built.Manifest().Entries() {
				if entry.Path == tc.link || strings.HasPrefix(entry.Path, tc.link+"/") {
					t.Errorf("the manifest holds %q, which is the accepted link or below it", entry.Path)
				}
			}
		})
	}
}
