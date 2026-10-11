package scope

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// defaults are the fixed protected patterns of D-SCOPE-DEFAULTS.
var defaults = []Pattern{mustParsePattern("/" + DeclarationPath), mustParsePattern("/.github/workflows/**")}

// mustParsePattern compiles a constant pattern; failure is a programming error.
func mustParsePattern(s string) Pattern {
	p, err := ParsePattern(s)
	if err != nil {
		panic(err)
	}
	return p
}

// Scope is the effective protected scope of a declaration: the fixed
// defaults plus include plus runner, minus exclude, with the declaration
// file itself always covered.
type Scope struct {
	declaration Declaration
}

// NewScope computes the effective scope of d.
func NewScope(d Declaration) Scope {
	return Scope{declaration: d}
}

// Covers reports whether path, a repository-relative file path, is in the
// effective scope. An invalid path returns artifact.ErrInvalidPath.
func (s Scope) Covers(path string) (bool, error) {
	if !artifact.ValidPath(path) {
		return false, fmt.Errorf("%w: %q", artifact.ErrInvalidPath, path)
	}
	if path == DeclarationPath {
		// No exclude removes the declaration file (D-SCOPE-DEFAULTS).
		return true, nil
	}
	excluded, err := matchAny(s.declaration.exclude, path)
	if err != nil || excluded {
		return false, err
	}
	for _, list := range [][]Pattern{defaults, s.declaration.include, s.declaration.runner} {
		if covered, err := matchAny(list, path); err != nil || covered {
			return covered, err
		}
	}
	return false, nil
}

func matchAny(patterns []Pattern, path string) (bool, error) {
	for _, p := range patterns {
		matched, err := p.Match(path)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

// Declaration returns the declaration the scope was computed from.
func (s Scope) Declaration() Declaration {
	return s.declaration
}

// Digest identifies the raw declaration bytes plus the normalized rules, as
// D-SCOPE requires approval to bind. Encoding v1: the domain separator, a
// presence byte, the length-prefixed raw bytes, then the defaults, include,
// exclude and runner lists, each a big-endian uint64 count followed by its
// bytewise-sorted, length-prefixed pattern strings.
func (s Scope) Digest() artifact.Digest {
	encoded := []byte("suiteward.scope.v1\x00")
	presence := byte(0)
	if s.declaration.present {
		presence = 1
	}
	encoded = append(encoded, presence)
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(s.declaration.raw)))
	encoded = append(encoded, s.declaration.raw...)
	for _, list := range [][]Pattern{defaults, s.declaration.include, s.declaration.exclude, s.declaration.runner} {
		sorted := patternSet(list)
		encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(sorted)))
		for _, p := range sorted {
			encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(p)))
			encoded = append(encoded, p...)
		}
	}
	return artifact.Hash(encoded)
}

// Reductions lists the rule-level scope reductions from governing to
// proposed, sorted and never nil: "rule <p>" for each protecting pattern
// (default, include or runner) of governing that proposed no longer
// protects with, and "exclude <p>" for each exclude pattern proposed adds.
// It is syntactic: moving a pattern between include and runner is not a
// reduction, and file-level effects are not considered.
func Reductions(governing, proposed Scope) []string {
	reductions := []string{}
	protecting := patternSet(proposed.protecting())
	for _, p := range patternSet(governing.protecting()) {
		if !slices.Contains(protecting, p) {
			reductions = append(reductions, "rule "+p)
		}
	}
	excluded := patternSet(governing.declaration.exclude)
	for _, p := range patternSet(proposed.declaration.exclude) {
		if !slices.Contains(excluded, p) {
			reductions = append(reductions, "exclude "+p)
		}
	}
	slices.Sort(reductions)
	return reductions
}

func (s Scope) protecting() []Pattern {
	return slices.Concat(defaults, s.declaration.include, s.declaration.runner)
}

// patternSet returns the distinct pattern strings of patterns, sorted bytewise.
func patternSet(patterns []Pattern) []string {
	set := make([]string, 0, len(patterns))
	for _, p := range patterns {
		set = append(set, p.String())
	}
	slices.Sort(set)
	return slices.Compact(set)
}
