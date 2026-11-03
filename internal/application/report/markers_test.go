package report_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

// markersOf is the markers of one file whose change is the removal of lines from "keep\n" (when remove is set) or
// their addition to it.
func markersOf(t *testing.T, path, line string, remove bool) []report.Marker {
	t.Helper()
	c := mod(path, "keep\n", "keep\n"+line+"\n")
	if remove {
		c = mod(path, "keep\n"+line+"\n", "keep\n")
	}
	return one(t, c).Markers
}

func TestMarkersPerPattern(t *testing.T) {
	t.Parallel()
	const (
		assertion   = report.MarkRemovedAssertion
		skip        = report.MarkAddedSkip
		focus       = report.MarkFocus
		test        = report.MarkDeletedTest
		removedCase = report.MarkerKind("removed-case")
	)
	cases := []struct {
		path, line string
		remove     bool // the line is removed, otherwise added
		want       report.MarkerKind
	}{
		{"x_test.go", "\tassert.Equal(t, 1, 1)", true, assertion},
		{"x_test.go", "\trequire.NoError(t, err)", true, assertion},
		{"x_test.go", "\tt.Error(\"bad\")", true, assertion},
		{"x_test.go", "\tt.Errorf(\"bad %d\", 1)", true, assertion},
		{"x_test.go", "\tt.Fatal(\"bad\")", true, assertion},
		{"x_test.go", "\tt.Fatalf(\"bad %d\", 1)", true, assertion},
		{"x_test.go", "\tt.Skip(\"later\")", false, skip},
		{"x_test.go", "\tt.Skipf(\"later %d\", 1)", false, skip},
		{"x_test.go", "func TestFoo(t *testing.T) {", true, test},
		{"x_test.go", "\tb.Error(\"bad\")", true, assertion},
		{"x_test.go", "\tb.Fatalf(\"bad\")", true, assertion},
		{"x_test.go", "\ttb.Error(\"bad\")", true, assertion},
		{"x_test.go", "\ttb.Fatalf(\"bad\")", true, assertion},
		{"x_test.go", "\tt.Fail()", true, assertion},
		{"x_test.go", "\tt.FailNow()", true, assertion},
		{"x_test.go", "\tb.Skip(\"later\")", false, skip},
		{"x_test.go", "\ttb.Skipf(\"later\")", false, skip},
		{"x_test.go", "func (s *Suite) TestFoo() {", true, test},

		{"x_test.go", "\t\"_octo\": false,", true, removedCase},
		{"x_test.go", "\t\"a b\": {want: 1},", true, removedCase},
		{"x_test.go", "\t\"a\\\"b\": 1,  \t", true, removedCase}, // an escaped quote in the key, and space after the comma
		{"x_test.go", "\t{name: \"x\", want: 1},", true, removedCase},
		{"x_test.go", "\t{\"x\", 1},", true, removedCase},
		{"x_test.go", "\t{\"x\", 1}", true, removedCase}, // the last row of a literal that is on one line has no comma
		{"tests/helpers.go", "\t\"a\": 1,", true, removedCase},

		{"test_x.py", "    assert x == 1", true, assertion},
		{"test_x.py", "    assert(x == 1)", true, assertion},
		{"test_x.py", "    self.skipTest(\"no\")", false, skip},
		{"test_x.py", "        self.assertEqual(a, b)", true, assertion},
		{"test_x.py", "    with pytest.raises(ValueError):", true, assertion},
		{"test_x.py", "    pytest.skip(\"no\")", false, skip},
		{"test_x.py", "@pytest.mark.skip(reason=\"no\")", false, skip},
		{"test_x.py", "@pytest.mark.skipif(True, reason=\"no\")", false, skip},
		{"test_x.py", "@unittest.skip(\"no\")", false, skip},
		{"test_x.py", "@skip", false, skip},
		{"test_x.py", "def test_foo():", true, test},
		{"test_x.py", "async def test_foo():", true, test},

		{"x.test.ts", "  expect(a).toBe(1)", true, assertion},
		{"x.test.ts", "  assert.equal(a, 1)", true, assertion},
		{"x.test.ts", "it.skip('x', () => {", false, skip},
		{"x.test.ts", "test.skip('x', () => {", false, skip},
		{"x.test.ts", "describe.skip('x', () => {", false, skip},
		{"x.test.ts", "xit('x', () => {", false, skip},
		{"x.test.ts", "xdescribe('x', () => {", false, skip},
		{"x.test.ts", "it.only('x', () => {", false, focus},
		{"x.test.ts", "describe.only('x', () => {", false, focus},
		{"x.test.ts", "it('does x', () => {", true, test},
		{"x.test.ts", "test('does y', () => {", true, test},
		{"x.test.ts", "describe('a group', () => {", true, test},
		{"x.test.ts", "xtest('x', () => {", false, skip},
		{"x.test.ts", "fit('x', () => {", false, focus},
		{"x.test.ts", "fdescribe('x', () => {", false, focus},
		{"x.test.ts", "  chai.expect(a).to.equal(1)", true, assertion},

		{"x.rb", "assert_equal 1, 1", true, assertion},
		{"x.txt", "  assert result", true, assertion},
	}
	for _, tc := range cases {
		got := markersOf(t, tc.path, tc.line, tc.remove)
		line := 2
		want := []report.Marker{{Kind: tc.want, Path: tc.path, Line: line, Text: strings.TrimSpace(tc.line)}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %q (removed=%v): got %+v, want %+v", tc.path, tc.line, tc.remove, got, want)
		}
	}
}

func TestMarkersAreKeyedByLanguage(t *testing.T) {
	t.Parallel()
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".JS"} {
		got := markersOf(t, "x"+ext, "expect(a).toBe(1)", true)
		if len(got) != 1 || got[0].Kind != report.MarkRemovedAssertion {
			t.Errorf("%s: got %+v, want a removed assertion", ext, got)
		}
	}
	for _, c := range []struct {
		path, line string
		remove     bool
	}{
		{"x.rb", "expect(a).to eq(1)", true},            // the fallback knows only assert
		{"x.rb", "it.skip('x')", false},                 // no skip in the fallback
		{"x.py", "t.Skip(\"x\")", false},                // a Go idiom in a Python file
		{"x.go", "expect(a).toBe(1)", true},             // a JavaScript idiom in a Go file
		{"x.go", "it.only('x')", false},                 // focus is a JavaScript idiom
		{"x.txt", "func TestFoo(t *testing.T) {", true}, // no test declaration in the fallback
		{"x.go", "\tif t.Failed() {", true},             // a query, not a failure
		{"x.go", "func (s *Suite) setup() {", true},     // a method that is no test
		{"x.go", "\tt.Run(\"(a) Test\", f)", true},      // the text of a test name
		{"x.ts", "if (/x/.test(s)) {", true},            // a regular expression, not a test
		{"x.ts", "foo.it('x')", true},                   // a method, not the test function
		{"x.ts", "model.fit(x, y)", false},              // a method, not a focused test
		{"x.ts", "page.xit('x')", false},                // likewise for a skip

		{"x.go", "\t\"_octo\": false,", true},                      // a table row of a Go file that is no test file
		{"x.go", "\t{name: \"x\", want: 1},", true},                // likewise for a struct literal row
		{"test_x.py", "    \"a\": 1,", true},                       // Go only: a removed entry of a Python test
		{"test_x.py", "    {\"a\": 1},", true},                     // likewise for a literal row
		{"x.test.ts", "  \"a\": 1,", true},                         // and of a TypeScript test
		{"x.test.ts", "  {a: 1},", true},                           // and a literal row of it
		{"x_test.go", "\t\"_octo\": false,", false},                // an added row is no marker
		{"x_test.go", "\t{\"x\", 1},", false},                      // likewise for an added literal row
		{"x_test.go", "\t{", true},                                 // the opening of a case that spans lines
		{"x_test.go", "\t},", true},                                // and its end
		{"x_test.go", "\t}", true},                                 // a closing brace
		{"x_test.go", "\t\"_octo\": false", true},                  // an entry has its comma
		{"x_test.go", "\tname: \"x\",", true},                      // a field, not a keyed entry of a table
		{"x_test.go", "\t\"a b\",", true},                          // an element, not an entry
		{"x_test.go", "\tt.Run(\"a\", func(t *testing.T) {", true}, // a call
	} {
		if got := markersOf(t, c.path, c.line, c.remove); len(got) != 0 {
			t.Errorf("%s %q: got %+v, want none", c.path, c.line, got)
		}
	}
}

func TestMarkersOnlyLookAtChangedLinesOfTheRightSide(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, old, proposed string
	}{
		{"an unchanged assertion", "x.py", "assert a\nb\n", "assert a\nc\n"},
		{"an added assertion", "x.py", "a\n", "a\nassert b\n"},
		{"a removed skip", "x.py", "pytest.skip('x')\na\n", "a\n"},
		{"an added test declaration", "x.py", "a\n", "a\ndef test_x():\n"},
		{"an unchanged test declaration", "x.py", "def test_x():\na\n", "def test_x():\nb\n"},
		{"a removed focus", "x.ts", "it.only('x', f)\na\n", "a\n"},
	} {
		if got := one(t, mod(tc.path, tc.old, tc.proposed)).Markers; len(got) != 0 {
			t.Errorf("%s: got %+v, want none", tc.name, got)
		}
	}
}

func TestMarkersIgnoreLinesThatOnlyMoved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, old, proposed string
	}{
		{"an assertion moved", "x.py", "assert a\nb\nc\n", "b\nc\nassert a\n"},
		{"an assertion reindented", "x.py", "assert a\nb\n", "    assert a\nb\n"},
		{"CRLF turned into LF", "x.py", "assert a\r\nb\r\n", "assert a\nb\n"},
		{"a test declaration moved", "x.go", "func TestA(t *testing.T) {}\nfunc B() {}\n", "func B() {}\nfunc TestA(t *testing.T) {}\n"},
		{"a skip moved", "x.py", "pytest.skip('x')\na\nb\n", "a\nb\n    pytest.skip('x')\n"},
		{"a focus reindented", "x.ts", "a\nit.only('x', f)\n", "  a\n  it.only('x', f)\n"},
	} {
		if got := one(t, mod(tc.path, tc.old, tc.proposed)).Markers; len(got) != 0 {
			t.Errorf("%s: got %+v, want none", tc.name, got)
		}
	}
	// Two copies of an assertion go and one comes back: one removal is left to see.
	got := one(t, mod("x.py", "assert a\nx\nassert a\ny\n", "x\ny\nassert a\n")).Markers
	if len(got) != 1 || got[0].Kind != report.MarkRemovedAssertion {
		t.Errorf("got %+v, want one removed assertion", got)
	}
}

func TestMarkerLineNumbers(t *testing.T) {
	t.Parallel()
	got := one(t, mod("x.py", "a\nb\nassert 1\nc\n", "x\ny\na\nb\nc\npytest.skip('s')\n")).Markers
	want := []report.Marker{
		{Kind: report.MarkRemovedAssertion, Path: "x.py", Line: 3, Text: "assert 1"},  // the old side's number
		{Kind: report.MarkAddedSkip, Path: "x.py", Line: 6, Text: "pytest.skip('s')"}, // the new side's number
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMarkerIdentifierBoundary(t *testing.T) {
	t.Parallel()
	if got := markersOf(t, "x.go", "\treassert(t)", true); len(got) != 0 {
		t.Errorf("assert inside an identifier is no assertion: got %+v", got)
	}
	if got := markersOf(t, "x.go", "\tx := assertThat(y)", true); len(got) != 1 {
		t.Errorf("an identifier that starts with assert is one: got %+v", got)
	}
	if got := markersOf(t, "x.ts", "submit(form)", true); len(got) != 0 {
		t.Errorf("it( inside submit( is no test declaration: got %+v", got)
	}
	if got := markersOf(t, "x.ts", "it.only('x', f)", false); len(got) != 1 {
		t.Errorf(".only( follows an identifier and still counts: got %+v", got)
	}
}

func TestMarkersOfOneLineKeepAKindOrder(t *testing.T) {
	t.Parallel()
	got := markersOf(t, "x.ts", "it('x', () => expect(1).toBe(1))", true)
	want := []report.Marker{
		{Kind: report.MarkRemovedAssertion, Path: "x.ts", Line: 2, Text: "it('x', () => expect(1).toBe(1))"},
		{Kind: report.MarkDeletedTest, Path: "x.ts", Line: 2, Text: "it('x', () => expect(1).toBe(1))"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMarkersOfARowThatAssertsKeepAKindOrder(t *testing.T) {
	t.Parallel()
	got := markersOf(t, "x_test.go", "\t{assert.True(t, x)},", true)
	want := []report.Marker{
		{Kind: report.MarkRemovedAssertion, Path: "x_test.go", Line: 2, Text: "{assert.True(t, x)},"},
		{Kind: "removed-case", Path: "x_test.go", Line: 2, Text: "{assert.True(t, x)},"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMarkerOfARemovedRowIgnoresTheLineEnding(t *testing.T) {
	t.Parallel()
	got := one(t, mod("x_test.go", "keep\r\n\t\"_octo\": false,\r\n", "keep\r\n")).Markers
	want := []report.Marker{{Kind: "removed-case", Path: "x_test.go", Line: 2, Text: "\"_octo\": false,"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMarkersIgnoreARowThatOnlyMoved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, old, proposed string
	}{
		{"a row moved", "\t\"a\": 1,\n\t\"b\": 2,\n\tx\n", "\t\"b\": 2,\n\tx\n\t\"a\": 1,\n"},
		{"a row reindented", "\"a\": 1,\nx\n", "\t\"a\": 1,\nx\n"},
		{"a literal row moved", "\t{\"a\", 1},\n\t{\"b\", 2},\n\tx\n", "\t{\"b\", 2},\n\tx\n\t{\"a\", 1},\n"},
	} {
		if got := one(t, mod("x_test.go", tc.old, tc.proposed)).Markers; len(got) != 0 {
			t.Errorf("%s: got %+v, want none", tc.name, got)
		}
	}
}

// The case of the M2.1b acceptance test (a pull request removed the row "_octo" of TestValidLogin): one marker, and no other.
func TestMarkerOfARemovedCaseOfATableTest(t *testing.T) {
	t.Parallel()
	const (
		head = "package commandtext_test\n\nfunc TestValidLogin(t *testing.T) {\n\tfor login, want := range map[string]bool{\n\t\t\"octo-owner\": true,\n"
		row  = "\t\t\"_octo\":      false,\n"
		tail = "\t\t\"-octo\":      false,\n\t} {\n\t\tif got := commandtext.ValidLogin(login); got != want {\n\t\t\tt.Errorf(\"ValidLogin(%q) = %v, want %v\", login, got, want)\n\t\t}\n\t}\n}\n"
	)
	got := one(t, mod("ack_test.go", head+row+tail, head+tail)).Markers
	want := []report.Marker{{Kind: "removed-case", Path: "ack_test.go", Line: 6, Text: "\"_octo\":      false,"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMarkerTextIsTrimmedAndCut(t *testing.T) {
	t.Parallel()
	if got := markersOf(t, "x.py", "\t\tassert x \r", true); len(got) != 1 || got[0].Text != "assert x" {
		t.Errorf("got %+v, want the trimmed line", got)
	}
	// The match is on the whole line: the assertion sits past the stored line limit of 500 bytes.
	line := strings.Repeat("é", 300) + " assert x"
	got := markersOf(t, "x.py", line, true)
	if len(got) != 1 || got[0].Kind != report.MarkRemovedAssertion {
		t.Fatalf("got %+v, want a removed assertion found past 500 bytes", got)
	}
	if want := strings.Repeat("é", 100); got[0].Text != want {
		t.Errorf("Text = %d bytes, want 200 bytes cut on a rune start", len(got[0].Text))
	}
}

func TestMarkerOfARemovedTestFile(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"a_test.go", "pkg/a_test.go", "test_a.py", "a_test.py", "a.test.ts", "a.spec.js", "tests/data.json", "src/__tests__/a.js", "src/test/java/A.java",
		"spec/models/user_spec.rb", "lib/user_spec.rb", "spec/helper.js", "src/UserTest.java", "conftest.py", "pkg/conftest.py", "Tests/data.txt", "Test/x.cs"} {
		got := one(t, removed(path, "x\n")).Markers
		want := []report.Marker{{Kind: report.MarkTestFileRemoved, Path: path}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", path, got, want)
		}
	}
	for _, c := range []report.FileChange{
		removed("main.go", "x\n"), removed("a_test.txt", "x\n"), removed("contest.py", "x\n"), removed("latest/a.go", "x\n"),
		removed("src/Contest.java", "x\n"), removed("specs/a.go", "x\n"), removed("myconftest.py", "x\n"), removed("lib/user.rb", "x\n"),
		mod("a_test.go", "x\n", "y\n"), added("b_test.go", "x\n"),
	} {
		if got := one(t, c).Markers; len(got) != 0 {
			t.Errorf("%s %s: got %+v, want no marker", c.Status, c.Path, got)
		}
	}
}

func TestMarkerOfARemovedTestFileSurvivesNoDiff(t *testing.T) {
	t.Parallel()
	want := []report.Marker{{Kind: report.MarkTestFileRemoved, Path: "a_test.go"}}
	if got := one(t, removed("a_test.go", "\x00")).Markers; !reflect.DeepEqual(got, want) {
		t.Errorf("binary: got %+v, want %+v", got, want)
	}
	if got := one(t, removed("a_test.go", strings.Repeat("assert x\n", 20_001))).Markers; !reflect.DeepEqual(got, want) {
		t.Errorf("too large: got %+v, want %+v", got, want)
	}
}

func TestMarkersOfARemovedFileFollowItsLines(t *testing.T) {
	t.Parallel()
	got := one(t, removed("a_test.go", "func TestA(t *testing.T) {\n\tassert.True(t, ok)\n}\n")).Markers
	want := []report.Marker{
		{Kind: report.MarkTestFileRemoved, Path: "a_test.go"},
		{Kind: report.MarkDeletedTest, Path: "a_test.go", Line: 1, Text: "func TestA(t *testing.T) {"},
		{Kind: report.MarkRemovedAssertion, Path: "a_test.go", Line: 2, Text: "assert.True(t, ok)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
