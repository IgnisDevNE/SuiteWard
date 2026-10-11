// Package scope matches repository-relative file paths against the
// gitignore-style include/exclude rules of D-SCOPE
// (docs/plan/decisions.md).
package scope

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ErrInvalidPattern reports a pattern that does not satisfy the D-SCOPE
// gitignore subset.
var ErrInvalidPattern = errors.New("invalid scope pattern")

// Pattern is a validated gitignore-style scope rule.
type Pattern struct {
	raw      string
	segments []string
	isDir    bool
}

// ParsePattern validates and compiles a gitignore-style pattern.
//
// Semantics (the D-SCOPE gitignore subset): case-sensitive matching with "/"
// as the only separator; a leading "/" or an interior "/" (anywhere before
// the pattern's last character) anchors the pattern to the repository root,
// otherwise it matches at any depth; a trailing "/" restricts the pattern to
// files beneath a matching directory; "**" as a whole segment matches zero
// or more path segments, while "*", "?" and "[...]" follow path.Match within
// a single segment.
func ParsePattern(s string) (Pattern, error) {
	if strings.TrimSpace(s) == "" {
		return Pattern{}, fmt.Errorf("%w: empty or whitespace-only pattern", ErrInvalidPattern)
	}
	if s[0] == '!' {
		return Pattern{}, fmt.Errorf("%w: negated pattern %q is not supported", ErrInvalidPattern, s)
	}
	if strings.ContainsRune(s, '\\') {
		return Pattern{}, fmt.Errorf("%w: backslash in %q", ErrInvalidPattern, s)
	}
	if s == "/" {
		return Pattern{}, fmt.Errorf("%w: %q names no segment", ErrInvalidPattern, s)
	}

	isDir := strings.HasSuffix(s, "/")
	anchored := s[0] == '/' || strings.Contains(s[:len(s)-1], "/")

	body := s
	if isDir {
		body = body[:len(body)-1]
	}
	body = strings.TrimPrefix(body, "/")

	segments := strings.Split(body, "/")
	for _, segment := range segments {
		switch {
		case segment == "" || segment == "." || segment == "..":
			return Pattern{}, fmt.Errorf("%w: empty, \".\" or \"..\" segment in %q", ErrInvalidPattern, s)
		case segment == "**":
			// A whole "**" segment is the recursive wildcard; no further validation.
		case strings.Contains(segment, "**"):
			return Pattern{}, fmt.Errorf("%w: %q mixes ** with other characters in %q", ErrInvalidPattern, segment, s)
		default:
			if _, err := path.Match(segment, ""); err != nil {
				return Pattern{}, fmt.Errorf("%w: %q: %v", ErrInvalidPattern, s, err)
			}
		}
	}
	if !anchored {
		segments = append([]string{"**"}, segments...)
	}

	return Pattern{raw: s, segments: segments, isDir: isDir}, nil
}

// Match reports whether path, a repository-relative, "/"-separated file
// path, is covered by p. When path is not a valid manifest path it returns
// artifact.ErrInvalidPath, never a silent false.
func (p Pattern) Match(path string) (bool, error) {
	if !artifact.ValidPath(path) {
		return false, fmt.Errorf("%w: %q", artifact.ErrInvalidPath, path)
	}
	components := strings.Split(path, "/")
	for _, consumed := range matchEnds(p.segments, components) {
		if consumed < len(components) {
			// The pattern matched a directory; every file beneath it matches (rule 4).
			return true, nil
		}
		if !p.isDir {
			// The pattern consumed the whole path: a file match, unless the
			// pattern names a directory (trailing "/"), which never matches
			// a file of that same name.
			return true, nil
		}
	}
	return false, nil
}

// IsZero reports whether the pattern is absent.
func (p Pattern) IsZero() bool {
	panic("not implemented")
}

// String returns the validated pattern's original source text, unnormalized.
func (p Pattern) String() string {
	return p.raw
}

// matchEnds returns every component count consumed by a full alignment of
// segments against the start of components, honoring "**" as zero or more
// segments. It is memoized because adjacent "**" segments would otherwise
// revisit the same (segment, component) position many times.
func matchEnds(segments, components []string) []int {
	type state struct{ segment, component int }
	ends := make(map[int]struct{})
	visited := make(map[state]bool)

	var walk func(segment, component int)
	walk = func(segment, component int) {
		key := state{segment, component}
		if visited[key] {
			return
		}
		visited[key] = true

		if segment == len(segments) {
			ends[component] = struct{}{}
			return
		}
		if segments[segment] == "**" {
			for next := component; next <= len(components); next++ {
				walk(segment+1, next)
			}
			return
		}
		if component >= len(components) {
			return
		}
		if ok, _ := path.Match(segments[segment], components[component]); ok {
			walk(segment+1, component+1)
		}
	}
	walk(0, 0)

	result := make([]int, 0, len(ends))
	for end := range ends {
		result = append(result, end)
	}
	return result
}
