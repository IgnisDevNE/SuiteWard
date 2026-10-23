package report

import (
	"path"
	"strings"
)

// rules are the strings that mark a line in one language, each looked for in the whole line. A pattern that starts
// with a letter, digit or underscore must not continue an identifier ("assert" is in "assertEqual" but not in
// "reassert"); the others (".only(", "@skip") may follow anything.
type rules struct{ assertion, skip, focus, test []string }

var (
	goRules     = rules{assertion: []string{"assert", "require.", "t.Error", "t.Fatal"}, skip: []string{"t.Skip"}, test: []string{"func Test"}}
	pythonRules = rules{
		assertion: []string{"assert ", "self.assert", "pytest.raises"},
		skip:      []string{"pytest.skip", "@pytest.mark.skip", "@unittest.skip", "@skip"},
		test:      []string{"def test_"},
	}
	scriptRules = rules{
		assertion: []string{"expect(", "assert"},
		skip:      []string{"it.skip", "test.skip", "describe.skip", "xit(", "xdescribe("},
		focus:     []string{".only("},
		test:      []string{"it(", "test("},
	}
	// The rules of every other extension: an assertion is all they can tell.
	fallbackRules = rules{assertion: []string{"assert"}}
)

func rulesFor(file string) rules {
	switch strings.ToLower(path.Ext(file)) {
	case ".go":
		return goRules
	case ".py":
		return pythonRules
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return scriptRules
	}
	return fallbackRules
}

// kinds are the markers of one changed line: a removed line can be an assertion or a test declaration, an added
// line a skip or a focus.
func (r rules) kinds(k LineKind, line string) []MarkerKind {
	var out []MarkerKind
	switch k {
	case LineRemoved:
		if found(line, r.assertion) {
			out = append(out, MarkRemovedAssertion)
		}
		if found(line, r.test) {
			out = append(out, MarkDeletedTest)
		}
	case LineAdded:
		if found(line, r.skip) {
			out = append(out, MarkAddedSkip)
		}
		if found(line, r.focus) {
			out = append(out, MarkFocus)
		}
	}
	return out
}

func found(line string, patterns []string) bool {
	for _, p := range patterns {
		for from := 0; ; {
			i := strings.Index(line[from:], p)
			if i < 0 {
				break
			}
			i += from
			if !isIdentifier(p[0]) || i == 0 || !isIdentifier(line[i-1]) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

func isIdentifier(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// isTestPath says whether path looks like a test file of the languages above, or sits in a directory of tests.
func isTestPath(file string) bool {
	for _, dir := range strings.Split(path.Dir(file), "/") {
		if dir == "test" || dir == "tests" || dir == "__tests__" {
			return true
		}
	}
	ext := path.Ext(file)
	stem := strings.TrimSuffix(path.Base(file), ext)
	switch strings.ToLower(ext) {
	case ".go":
		return strings.HasSuffix(stem, "_test")
	case ".py":
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test")
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
	}
	return false
}
