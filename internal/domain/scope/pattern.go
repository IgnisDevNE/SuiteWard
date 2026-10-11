// Package scope matches repository-relative file paths against the
// gitignore-style include/exclude rules of D-SCOPE
// (docs/plan/decisions.md).
package scope

import "errors"

// ErrInvalidPattern reports a pattern that does not satisfy the D-SCOPE
// gitignore subset.
var ErrInvalidPattern = errors.New("invalid scope pattern")

// Pattern is a validated gitignore-style scope rule.
type Pattern struct{}

// ParsePattern validates and compiles a gitignore-style pattern.
func ParsePattern(s string) (Pattern, error) {
	return Pattern{}, errors.New("scope: ParsePattern not implemented")
}

// Match reports whether path, a repository-relative file path, is covered by p.
func (p Pattern) Match(path string) (bool, error) {
	return false, errors.New("scope: Match not implemented")
}

// String returns the validated pattern's original source text.
func (p Pattern) String() string {
	panic("not implemented")
}
