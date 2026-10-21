package inventory

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The entries of a built tree are valid paths; these helpers are also reachable by a caller that passes its own, and each
// refuses what is no path of the repository instead of treating it as uncovered.
func TestNewColliderRefusesAnInvalidPath(t *testing.T) {
	_, err := newCollider([]Entry{{Path: "../x", Kind: File}}, scope.NewScope(scope.NoDeclaration()))
	if !errors.Is(err, artifact.ErrInvalidPath) {
		t.Fatalf("newCollider error = %v, want ErrInvalidPath", err)
	}
}

func TestUnsafeLinkRefusesAnInvalidLink(t *testing.T) {
	_, err := unsafeLink(t.Context(), nil, layout{}, nil, scope.NewScope(scope.NoDeclaration()), collider{}, "../x", false)
	if !errors.Is(err, artifact.ErrInvalidPath) {
		t.Fatalf("unsafeLink error = %v, want ErrInvalidPath", err)
	}
}

func TestUnsafeLinkRefusesAnInvalidRewrite(t *testing.T) {
	collisions := collider{dirs: map[string]map[string]bool{foldKey("a"): {"a": true, "A:": true}}}
	_, err := unsafeLink(t.Context(), nil, layout{}, nil, scope.NewScope(scope.NoDeclaration()), collisions, "a/f", false)
	if !errors.Is(err, artifact.ErrInvalidPath) {
		t.Fatalf("unsafeLink error = %v, want ErrInvalidPath", err)
	}
}
