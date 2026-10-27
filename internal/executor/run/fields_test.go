package run

import (
	"errors"
	"strings"
	"testing"
)

// The field bounds of POST /jobs/{id}/result (docs/contracts/runtime.md): a package at most 512 bytes, a test name 200, a test file and an
// unparsed file 1024, and a marker's file and a submodule, which are paths of the tree, 4096. A path that holds a control, format, line
// separator or paragraph separator character is refused by the service, so the executor says why before it posts.
func TestAResultIsCheckedAgainstTheFieldBoundsOfThePostBeforeItIsPosted(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	test := func(pkg, name, file string) completion {
		return completion{tests: []testRow{{Package: pkg, Name: name, File: file, Kind: "declared"}}}
	}
	marker := func(path string, line int) completion {
		return completion{markers: []markerRow{{Kind: "env-read", Path: path, Line: line}}}
	}
	for name, tc := range map[string]struct {
		done   completion
		reason string // empty when the result is fine
	}{
		"a package at its bound":              {test(long(512), "TestA", "a_test.go"), ""},
		"a package past its bound":            {test(long(513), "TestA", "a_test.go"), reasonTooLarge},
		"a name at its bound":                 {test(".", "Test"+long(196), "a_test.go"), ""},
		"a name past its bound":               {test(".", "Test"+long(197), "a_test.go"), reasonTooLarge},
		"a test file at its bound":            {test(".", "TestA", long(1024-len("_test.go"))+"_test.go"), ""},
		"a test file past its bound":          {test(".", "TestA", long(1025-len("_test.go"))+"_test.go"), reasonTooLarge},
		"a marker's file at its bound":        {marker(long(4096), 1), ""},
		"a marker's file past its bound":      {marker(long(4097), 1), reasonTooLarge},
		"a marker at the first line":          {marker("a.go", 1), ""},
		"a marker at the last line":           {marker("a.go", 10_000_000), ""},
		"a marker before the first line":      {marker("a.go", 0), reasonTooLarge},
		"a marker past the last line":         {marker("a.go", 10_000_001), reasonTooLarge},
		"a submodule at its bound":            {completion{submodules: []string{long(4096)}}, ""},
		"a submodule past its bound":          {completion{submodules: []string{long(4097)}}, reasonTooLarge},
		"an unparsed file at its bound":       {completion{unparsed: []string{long(1024-len("_test.go")) + "_test.go"}}, ""},
		"an unparsed file past its bound":     {completion{unparsed: []string{long(1025-len("_test.go")) + "_test.go"}}, reasonTooLarge},
		"text and a space that are fine":      {test("pkg/ünï cöde", "TestÄ_1", "pkg/ünï cöde/ä_test.go"), ""},
		"a package with a NUL":                {test("a\x00b", "TestA", "a_test.go"), reasonSourceMismatch},
		"a test file with a newline":          {test(".", "TestA", "a\nb_test.go"), reasonSourceMismatch},
		"a test file with a bidi override":    {test(".", "TestA", "a"+bidiOverride+"b_test.go"), reasonSourceMismatch},
		"a package with a line separator":     {test("a"+lineSeparator+"b", "TestA", "a_test.go"), reasonSourceMismatch},
		"a marker's file with a format char":  {marker("a"+zeroWidthSpace+"b.go", 1), reasonSourceMismatch},
		"a marker's file with a tab":          {marker("a\tb.go", 1), reasonSourceMismatch},
		"a submodule with a paragraph break":  {completion{submodules: []string{"sub" + paragraphSeparator + "mod"}}, reasonSourceMismatch},
		"an unparsed file with a zero-width":  {completion{unparsed: []string{"a" + zeroWidthJoiner + "b_test.go"}}, reasonSourceMismatch},
		"a path both too long and with a NUL": {test(long(600)+"\x00", "TestA", "a_test.go"), reasonTooLarge},
	} {
		err := tc.done.check()
		if tc.reason == "" {
			if err != nil {
				t.Errorf("%s: check = %v; want a result that can be posted", name, err)
			}
			continue
		}
		var reason *reasonError
		if !errors.As(err, &reason) || reason.reason != tc.reason {
			t.Errorf("%s: check = %v; want %s", name, err, tc.reason)
		}
	}
}

func TestACheckNamesTheFieldAndNeverTheText(t *testing.T) {
	err := completion{tests: []testRow{{Package: ".", Name: "TestA", File: "secret" + bidiOverride + "_test.go", Kind: "declared"}}}.check()
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("check = %v; the candidate's text is not echoed", err)
	}
}

// The characters that a result cannot carry in a path, built from their code points so that no file holds them as text.
var (
	bidiOverride       = string(rune(0x202e)) // Cf
	zeroWidthSpace     = string(rune(0x200b)) // Cf
	zeroWidthJoiner    = string(rune(0x200d)) // Cf
	lineSeparator      = string(rune(0x2028)) // Zl
	paragraphSeparator = string(rune(0x2029)) // Zp
)
