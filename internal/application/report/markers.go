package report

import (
	"path"
	"regexp"
	"strings"
)

// rules are what marks a line in one language. Each pattern is looked for in the whole line. A pattern that starts
// with a letter, digit or underscore must not continue an identifier ("assert" is in "assertEqual" but not in
// "reassert"); the others (".only(", "@skip") may follow anything. A pattern written with a leading ^ is the name of a
// function that is called bare: it must not follow a "." either, so that "it(" is not in "regex.test(".
type rules struct {
	assertion, skip, focus []string
	declaresTest           func(line string) bool
}

var (
	goRules = rules{
		assertion:    []string{"assert", "require.", "t.Error", "t.Fatal", "tb.Error", "tb.Fatal", "b.Error", "b.Fatal", "t.Fail(", "t.FailNow("},
		skip:         []string{"t.Skip", "tb.Skip", "b.Skip"},
		declaresTest: goTest.MatchString,
	}
	pythonRules = rules{
		assertion:    []string{"assert ", "assert(", "self.assert", "pytest.raises"},
		skip:         []string{"pytest.skip", "@pytest.mark.skip", "@unittest.skip", "@skip", "self.skipTest("},
		declaresTest: func(line string) bool { return found(line, []string{"def test_"}) },
	}
	scriptRules = rules{
		assertion:    []string{"expect(", "assert"},
		skip:         []string{"it.skip", "test.skip", "describe.skip", "^xit(", "^xdescribe(", "^xtest("},
		focus:        []string{".only(", "^fit(", "^fdescribe("},
		declaresTest: func(line string) bool { return found(line, []string{"^it(", "^test(", "^describe("}) },
	}
	// The rules of every other extension: an assertion is all they can tell.
	fallbackRules = rules{assertion: []string{"assert"}, declaresTest: func(string) bool { return false }}

	// A Go test function, or a method of a test suite.
	goTest = regexp.MustCompile(`^\s*func\s+(\([^)]*\)\s*)?Test`)
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
		if r.declaresTest(line) {
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
		bare := strings.HasPrefix(p, "^")
		p = strings.TrimPrefix(p, "^")
		for from := 0; ; {
			i := strings.Index(line[from:], p)
			if i < 0 {
				break
			}
			i += from
			if !isIdentifier(p[0]) || !follows(line, i, bare) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

// follows says whether a match at line[i:] continues what is before it: an identifier, or, for a bare function, a
// method call.
func follows(line string, i int, bare bool) bool {
	if i == 0 {
		return false
	}
	c := line[i-1]
	return isIdentifier(c) || bare && c == '.'
}

func isIdentifier(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// isTestPath says whether file, a slash-separated path, looks like a test file of the languages above, or sits in a
// directory of tests or specs.
func isTestPath(file string) bool {
	for _, dir := range strings.Split(path.Dir(file), "/") {
		switch strings.ToLower(dir) {
		case "test", "tests", "__tests__", "spec":
			return true
		}
	}
	base := path.Base(file)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch strings.ToLower(ext) {
	case ".go":
		return strings.HasSuffix(stem, "_test")
	case ".py":
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") || base == "conftest.py"
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
	case ".rb":
		return strings.HasSuffix(stem, "_spec")
	case ".java":
		return strings.HasSuffix(stem, "Test")
	}
	return false
}
