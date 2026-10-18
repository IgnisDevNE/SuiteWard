package inventory_test

import (
	"context"
	"errors"
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
	governing := scope.NewScope(scope.NoDeclaration())

	_, err := inventory.Evaluate(context.Background(), newTree(t, symlink("tests/link")), governing)

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
