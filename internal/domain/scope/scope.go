package scope

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// Scope is the effective protected scope of a declaration.
type Scope struct{}

// NewScope computes the effective scope of d.
func NewScope(d Declaration) Scope {
	panic("not implemented")
}

// Covers reports whether path is in the effective scope.
func (s Scope) Covers(path string) (bool, error) {
	return false, errors.New("Covers: not implemented")
}

// Declaration returns the declaration the scope was computed from.
func (s Scope) Declaration() Declaration {
	panic("not implemented")
}

// Digest identifies the raw declaration bytes plus the normalized rules.
func (s Scope) Digest() artifact.Digest {
	panic("not implemented")
}

// Reductions lists the rule-level reductions from governing to proposed.
func Reductions(governing, proposed Scope) []string {
	panic("not implemented")
}
