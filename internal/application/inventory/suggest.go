package inventory

import (
	"errors"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// maxSuggestions bounds the include patterns offered to the owner.
const maxSuggestions = 20

var (
	testDirectories = []string{"tests", "test", "__tests__", "spec"}
	// testFileGlobs are the test-like file names, as a glob over the base name.
	testFileGlobs = []string{"*_test.*", "*.test.*", "*.spec.*", "test_*.py"}
)

// SuggestScope proposes include patterns from the test-like files of entries:
// "<dir>/**" for each matching top-level directory, and the file's own
// directory plus a glob for a test-named file. Only files count. The result is
// de-duplicated and never nil: the directory globs first, then the file globs,
// each group sorted. Past maxSuggestions the first are kept, so a directory glob
// is never dropped for a file glob and the answer does not depend on the entry order.
func SuggestScope(entries []Entry) []string {
	directories, files := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if entry.Kind != File {
			continue
		}
		if top, _, below := strings.Cut(entry.Path, "/"); below && slices.Contains(testDirectories, top) {
			suggest(directories, top+"/**", entry.Path)
		}
		dir, _ := path.Split(entry.Path)
		if strings.ContainsAny(dir, `*?[]\`) {
			continue // a glob character in a directory name would change what the pattern matches
		}
		for _, glob := range testFileGlobs {
			suggest(files, dir+glob, entry.Path) // recorded only when it matches this file
		}
	}
	patterns := append(append([]string{}, slices.Sorted(maps.Keys(directories))...), slices.Sorted(maps.Keys(files))...)
	return patterns[:min(len(patterns), maxSuggestions)]
}

// suggest records pattern in found when the scope parser accepts it and it matches
// source; anything else (for example a directory named "!x") is not offered.
func suggest(found map[string]bool, pattern, source string) {
	p, err := scope.ParsePattern(pattern)
	if err != nil {
		return
	}
	// Match fails only for a path the scope refuses, which no entry of a validated inventory has, so its error is unreachable here.
	if ok, err := p.Match(source); err == nil && ok {
		found[pattern] = true
	}
}

// Suggestion is a suggested declaration from file paths only.
type Suggestion struct {
	Include []string
	Runner  []string
	Mixed   []string
	Cut     bool
}

// Suggest replaces SuggestScope.
func Suggest(entries []Entry) Suggestion {
	return Suggestion{}
}

// Uncovered returns the test-like file paths of entries that s does not cover, sorted.
func Uncovered(entries []Entry, s scope.Scope) ([]string, error) {
	return nil, errors.New("not implemented")
}
