package inventory

import (
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
// sorted, de-duplicated and never nil; past maxSuggestions the first in sorted
// order are kept, so the answer does not depend on the entry order.
func SuggestScope(entries []Entry) []string {
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.Kind != File {
			continue
		}
		if top, _, below := strings.Cut(entry.Path, "/"); below && slices.Contains(testDirectories, top) {
			suggest(found, top+"/**", entry.Path)
		}
		dir, _ := path.Split(entry.Path)
		if strings.ContainsAny(dir, `*?[]\`) {
			continue // a glob character in a directory name would change what the pattern matches
		}
		for _, glob := range testFileGlobs {
			suggest(found, dir+glob, entry.Path) // recorded only when it matches this file
		}
	}
	patterns := make([]string, 0, len(found))
	for pattern := range found {
		patterns = append(patterns, pattern)
	}
	slices.Sort(patterns)
	return patterns[:min(len(patterns), maxSuggestions)]
}

// suggest records pattern when the scope parser accepts it and it matches
// source; anything else (for example a directory named "!x") is not offered.
func suggest(found map[string]bool, pattern, source string) {
	p, err := scope.ParsePattern(pattern)
	if err != nil {
		return
	}
	if ok, err := p.Match(source); err == nil && ok {
		found[pattern] = true
	}
}
