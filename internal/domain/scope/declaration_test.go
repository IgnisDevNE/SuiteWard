package scope_test

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// goldenDeclaration lists include out of order so the canonical digest
// encoding has to sort it.
const goldenDeclaration = "version: 1\ninclude:\n  - src/**\n  - Makefile\nexclude:\n  - src/gen/\nrunner:\n  - scripts/test.sh\n"

func TestParseDeclarationRejectsInvalidForms(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantPattern bool
	}{
		{name: "empty input", raw: ""},
		{name: "whitespace-only input", raw: " \n\t\n"},
		{name: "comment-only input", raw: "# nothing declared\n"},
		{name: "sequence document is not a mapping", raw: "- version\n"},
		{name: "scalar document is not a mapping", raw: "version\n"},
		{name: "unknown key", raw: "version: 1\nextra: []\n"},
		{name: "duplicate list key", raw: "version: 1\ninclude: [a]\ninclude: [b]\n"},
		{name: "duplicate version key", raw: "version: 1\nversion: 1\n"},
		{name: "missing version", raw: "include: [a]\n"},
		{name: "null version", raw: "version: ~\ninclude: [a]\n"},
		{name: "other integer version", raw: "version: 2\n"},
		{name: "zero version", raw: "version: 0\n"},
		{name: "string version", raw: "version: \"1\"\n"},
		{name: "float version", raw: "version: 1.0\n"},
		{name: "sequence version", raw: "version: [1]\n"},
		{name: "scalar include is not a sequence", raw: "version: 1\ninclude: src/\n"},
		{name: "null exclude is not a sequence", raw: "version: 1\nexclude: ~\n"},
		{name: "mapping runner is not a sequence", raw: "version: 1\nrunner: {a: b}\n"},
		{name: "integer item", raw: "version: 1\ninclude: [1]\n"},
		{name: "boolean item", raw: "version: 1\nexclude: [true]\n"},
		{name: "null item", raw: "version: 1\nrunner: [~]\n"},
		{name: "nested sequence item", raw: "version: 1\ninclude: [[a]]\n"},
		{name: "mapping item", raw: "version: 1\ninclude: [{a: b}]\n"},
		{name: "negated include pattern", raw: "version: 1\ninclude: ['!src/']\n", wantPattern: true},
		{name: "dot-dot exclude pattern", raw: "version: 1\nexclude: [../x]\n", wantPattern: true},
		{name: "empty runner pattern", raw: "version: 1\nrunner: ['']\n", wantPattern: true},
		{name: "same include pattern twice", raw: "version: 1\ninclude: [src/, src/]\n"},
		{name: "same exclude pattern twice", raw: "version: 1\nexclude: [a, b, a]\n"},
		{name: "same runner pattern twice", raw: "version: 1\nrunner: [Makefile, Makefile]\n"},
		{name: "two documents", raw: "version: 1\n---\nversion: 1\n"},
		{name: "second document that is not a declaration", raw: "version: 1\n---\n- a\n"},
		{name: "empty trailing document", raw: "version: 1\n---\n"},
		{name: "merge key alone", raw: "<<: {version: 1}\n"},
		{name: "merge key supplying include", raw: "version: 1\n<<: {include: [a]}\n"},
		{name: "merge key with a list of mappings", raw: "version: 1\n<<: [{include: [a]}, {exclude: [b]}]\n"},
		{name: "merge key after an explicit include", raw: "version: 1\ninclude: [a]\n<<: {include: [b]}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scope.ParseDeclaration([]byte(tt.raw))
			if !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want ErrInvalidDeclaration", tt.raw, err)
			}
			if got := errors.Is(err, scope.ErrInvalidPattern); got != tt.wantPattern {
				t.Fatalf("errors.Is(%v, ErrInvalidPattern) = %v, want %v", err, got, tt.wantPattern)
			}
		})
	}
}

func TestParseDeclarationRoundTrips(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantInclude []string
		wantExclude []string
		wantRunner  []string
	}{
		{
			name:        "every list in input order",
			raw:         goldenDeclaration,
			wantInclude: []string{"src/**", "Makefile"},
			wantExclude: []string{"src/gen/"},
			wantRunner:  []string{"scripts/test.sh"},
		},
		{
			name:        "version only",
			raw:         "version: 1\n",
			wantInclude: []string{}, wantExclude: []string{}, wantRunner: []string{},
		},
		{
			name:        "empty sequences",
			raw:         "version: 1\ninclude: []\nexclude: []\nrunner: []\n",
			wantInclude: []string{}, wantExclude: []string{}, wantRunner: []string{},
		},
		{
			name:        "explicit document start",
			raw:         "---\nversion: 1\ninclude: [docs/]\n",
			wantInclude: []string{"docs/"}, wantExclude: []string{}, wantRunner: []string{},
		},
		{
			name:        "same pattern in two lists",
			raw:         "version: 1\ninclude: [Makefile]\nrunner: [Makefile]\n",
			wantInclude: []string{"Makefile"}, wantExclude: []string{}, wantRunner: []string{"Makefile"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := scope.ParseDeclaration([]byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseDeclaration(%q) error = %v, want nil", tt.raw, err)
			}
			if !d.Present() {
				t.Fatal("Present() = false, want true")
			}
			if got := d.Raw(); !bytes.Equal(got, []byte(tt.raw)) {
				t.Fatalf("Raw() = %q, want %q", got, tt.raw)
			}
			if got := patternStrings(d.Include()); !reflect.DeepEqual(got, tt.wantInclude) {
				t.Fatalf("Include() = %q, want %q", got, tt.wantInclude)
			}
			if got := patternStrings(d.Exclude()); !reflect.DeepEqual(got, tt.wantExclude) {
				t.Fatalf("Exclude() = %q, want %q", got, tt.wantExclude)
			}
			if got := patternStrings(d.Runner()); !reflect.DeepEqual(got, tt.wantRunner) {
				t.Fatalf("Runner() = %q, want %q", got, tt.wantRunner)
			}
		})
	}
}

func TestDeclarationReturnsIndependentCopies(t *testing.T) {
	input := []byte(goldenDeclaration)
	d, err := scope.ParseDeclaration(input)
	if err != nil {
		t.Fatalf("ParseDeclaration error = %v, want nil", err)
	}
	other, err := scope.ParsePattern("other")
	if err != nil {
		t.Fatalf("ParsePattern error = %v, want nil", err)
	}

	input[0] = 'X'
	raw := d.Raw()
	raw[1] = 'X'
	d.Include()[0] = other
	d.Exclude()[0] = other
	d.Runner()[0] = other

	if got := d.Raw(); !bytes.Equal(got, []byte(goldenDeclaration)) {
		t.Fatalf("Raw() after mutation = %q, want %q", got, goldenDeclaration)
	}
	if got := patternStrings(d.Include()); !reflect.DeepEqual(got, []string{"src/**", "Makefile"}) {
		t.Fatalf("Include() after mutation = %q", got)
	}
	if got := patternStrings(d.Exclude()); !reflect.DeepEqual(got, []string{"src/gen/"}) {
		t.Fatalf("Exclude() after mutation = %q", got)
	}
	if got := patternStrings(d.Runner()); !reflect.DeepEqual(got, []string{"scripts/test.sh"}) {
		t.Fatalf("Runner() after mutation = %q", got)
	}
}

func TestNoDeclarationHasNoRules(t *testing.T) {
	d := scope.NoDeclaration()
	if d.Present() {
		t.Fatal("Present() = true, want false")
	}
	if got := d.Raw(); !reflect.DeepEqual(got, []byte(nil)) {
		t.Fatalf("Raw() = %#v, want nil", got)
	}
	if got := patternStrings(d.Include()); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("Include() = %q, want none", got)
	}
	if got := patternStrings(d.Exclude()); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("Exclude() = %q, want none", got)
	}
	if got := patternStrings(d.Runner()); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("Runner() = %q, want none", got)
	}
}

func TestDeclarationPathIsTheRootFile(t *testing.T) {
	if scope.DeclarationPath != ".suiteward.yml" {
		t.Fatalf("DeclarationPath = %q, want %q", scope.DeclarationPath, ".suiteward.yml")
	}
}

func patternStrings(patterns []scope.Pattern) []string {
	result := []string{}
	for _, p := range patterns {
		result = append(result, p.String())
	}
	return result
}

// repoDeclaration is this repository's own .suiteward.yml at 8346ee8, which mixes bare and double-quoted items.
const repoDeclaration = `version: 1
include:
  - "**/*_test.go"
  - "**/testdata/**"
  - scripts/test-*.ps1
runner:
  - scripts/ci.ps1
  - /codecov.yml
`

func mustParse(t *testing.T, raw []byte) scope.Declaration {
	t.Helper()
	d, err := scope.ParseDeclaration(raw)
	if err != nil {
		t.Fatalf("ParseDeclaration(%q) error = %v", raw, err)
	}
	return d
}

func TestAddPatternsKeepsTheOwnersFileAndAppends(t *testing.T) {
	got, err := scope.AddPatterns([]byte(repoDeclaration), []string{"*.spec.*", "scripts/test-*.ps1"}, []string{"jest.config.*"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `version: 1
include:
  - "**/*_test.go"
  - "**/testdata/**"
  - scripts/test-*.ps1
  - '*.spec.*'
runner:
  - scripts/ci.ps1
  - /codecov.yml
  - jest.config.*
`
	if string(got) != want {
		t.Fatalf("AddPatterns =\n%s\nwant\n%s", got, want)
	}
}

func TestAddPatternsCreatesMissingListsInBlockStyle(t *testing.T) {
	got, err := scope.AddPatterns([]byte("version: 1\n"), []string{"*_test.go", "testdata/"}, []string{"conftest.py"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "version: 1\ninclude:\n  - '*_test.go'\n  - testdata/\nrunner:\n  - conftest.py\n"
	if string(got) != want {
		t.Fatalf("AddPatterns = %q, want %q", got, want)
	}
	if got, err := scope.AddPatterns([]byte("version: 1\nexclude: [gen/]\n"), nil, []string{"a"}); err != nil || bytes.Contains(got, []byte("include")) {
		t.Fatalf("AddPatterns created an empty list: %q, %v", got, err)
	}
}

func TestAddPatternsKeepsComments(t *testing.T) {
	raw := "# Owner notes.\nversion: 1\ninclude:\n  - src/**   # keep this\n  # before docs\n  - docs/\nexclude:\n  - gen/\nrunner:\n  - a.sh\n# the end\n"
	got, err := scope.AddPatterns([]byte(raw), []string{"x_test.go"}, []string{"b.sh"})
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# Owner notes.", "# keep this", "# before docs", "# the end"} {
		if !bytes.Contains(got, []byte(comment)) {
			t.Errorf("AddPatterns dropped %q:\n%s", comment, got)
		}
	}
}

func TestAddPatternsWithNothingNewReturnsTheBytesUnchanged(t *testing.T) {
	raw := []byte("version:   1\n# comment\ninclude: [a,   b]\n")
	for name, args := range map[string][2][]string{
		"no patterns": {nil, nil}, "all present": {{"b", "a"}, nil}, "duplicates of a present one": {{"a", "a"}, {}},
	} {
		got, err := scope.AddPatterns(raw, args[0], args[1])
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%s: AddPatterns = %q, %v; want the input", name, got, err)
		}
	}
}

func TestAddPatternsRoundTrip(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		include, runner []string
		wantInclude     []string
		wantRunner      []string
	}{
		{"flow style lists stay valid", "version: 1\ninclude: [a]\nrunner: []\n", []string{"b"}, []string{"c"}, []string{"a", "b"}, []string{"c"}},
		{"a pattern already present is skipped, a repeated one is added once", "version: 1\ninclude:\n  - a\n", []string{"a", "x", "x", "y"}, nil, []string{"a", "x", "y"}, []string{}},
		{"a pattern may be in both lists", "version: 1\ninclude: [a]\n", nil, []string{"a"}, []string{"a"}, []string{"a"}},
		{"patterns that need quoting", "version: 1\n", []string{
			"*_test.go", "**/src/test/", "[ab]x", "a #b", "&x", "@x", "%x", "#x", "x: y", "true", "123", "~", "null", "- x", "a, b", "{x}", "'q'", `"dq"`, "? x", "|x", ">x",
		}, nil, []string{
			"*_test.go", "**/src/test/", "[ab]x", "a #b", "&x", "@x", "%x", "#x", "x: y", "true", "123", "~", "null", "- x", "a, b", "{x}", "'q'", `"dq"`, "? x", "|x", ">x",
		}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := mustParse(t, []byte(tt.raw))
			got, err := scope.AddPatterns([]byte(tt.raw), tt.include, tt.runner)
			if err != nil {
				t.Fatal(err)
			}
			d := mustParse(t, got)
			if got := patternStrings(d.Include()); !reflect.DeepEqual(got, tt.wantInclude) {
				t.Errorf("Include() = %q, want %q", got, tt.wantInclude)
			}
			if got := patternStrings(d.Runner()); !reflect.DeepEqual(got, tt.wantRunner) {
				t.Errorf("Runner() = %q, want %q", got, tt.wantRunner)
			}
			if !reflect.DeepEqual(patternStrings(d.Exclude()), patternStrings(original.Exclude())) {
				t.Errorf("Exclude() changed to %q", patternStrings(d.Exclude()))
			}
			if !strings.HasPrefix(strings.Join(patternStrings(d.Include()), "\n"), strings.Join(patternStrings(original.Include()), "\n")) {
				t.Errorf("Include() reordered the original patterns: %q", patternStrings(d.Include()))
			}
		})
	}
}

func TestAddPatternsRejectsInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{"empty": "", "wrong version": "version: 2\n", "no version": "include: [a]\n", "a duplicate": "version: 1\ninclude: [a, a]\n"} {
		if got, err := scope.AddPatterns([]byte(raw), []string{"x"}, nil); !errors.Is(err, scope.ErrInvalidDeclaration) || got != nil {
			t.Errorf("%s: AddPatterns = %q, %v; want ErrInvalidDeclaration", name, got, err)
		}
	}
	for _, pattern := range []string{"!x", "../x", `a\b`, "", " ", "a/**b"} {
		if got, err := scope.AddPatterns([]byte("version: 1\n"), []string{pattern}, nil); !errors.Is(err, scope.ErrInvalidPattern) || got != nil {
			t.Errorf("include %q: AddPatterns = %q, %v; want ErrInvalidPattern", pattern, got, err)
		}
		if got, err := scope.AddPatterns([]byte("version: 1\n"), nil, []string{pattern}); !errors.Is(err, scope.ErrInvalidPattern) || got != nil {
			t.Errorf("runner %q: AddPatterns = %q, %v; want ErrInvalidPattern", pattern, got, err)
		}
	}
}

// A runner pattern may name a file that does not exist yet (pytest.toml before pytest.ini): it must parse and cover.
func TestARunnerPatternThatMatchesNoFileIsValid(t *testing.T) {
	d := mustParse(t, []byte("version: 1\nrunner:\n  - pytest.toml\n  - conftest.py\n"))
	s := scope.NewScope(d)
	for path, want := range map[string]bool{"main.go": false, "src/tests/conftest.py": true, "pytest.toml": true, "a/pytest.ini": false} {
		if got, err := s.Covers(path); err != nil || got != want {
			t.Errorf("Covers(%q) = %v, %v; want %v", path, got, err, want)
		}
	}
}

// Live run 4: conftest.py went in unquoted among double-quoted items. A list whose items all share one quoting style gets its new items in it.
func TestAddPatternsFollowsTheQuotingOfTheListsItems(t *testing.T) {
	tests := []struct {
		name, items, want string
	}{
		{"double quotes", "  - \"a\"\n  - \"b\"\n", "  - \"a\"\n  - \"b\"\n  - \"conftest.py\"\n  - \"*_test.go\"\n"},
		{"single quotes", "  - 'a'\n  - 'b'\n", "  - 'a'\n  - 'b'\n  - 'conftest.py'\n  - '*_test.go'\n"},
		{"plain", "  - a\n  - b\n", "  - a\n  - b\n  - conftest.py\n  - '*_test.go'\n"}, // yaml.v3 itself quotes a leading star
		{"mixed", "  - \"a\"\n  - b\n", "  - \"a\"\n  - b\n  - conftest.py\n  - '*_test.go'\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scope.AddPatterns([]byte("version: 1\nrunner:\n"+tt.items), nil, []string{"conftest.py", "*_test.go"})
			if err != nil {
				t.Fatal(err)
			}
			if want := "version: 1\nrunner:\n" + tt.want; string(got) != want {
				t.Fatalf("AddPatterns =\n%s\nwant\n%s", got, want)
			}
			mustParse(t, got)
		})
	}
}

func TestAddExcludesAppendsToTheExcludeListAndCreatesIt(t *testing.T) {
	got, err := scope.AddExcludes([]byte("version: 1\ninclude:\n  - /tests/**\nexclude:\n  - gen/\n"), []string{"jest.config.js", "gen/", "packages/a/jest.config.js"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "version: 1\ninclude:\n  - /tests/**\nexclude:\n  - gen/\n  - jest.config.js\n  - packages/a/jest.config.js\n"; string(got) != want {
		t.Fatalf("AddExcludes = %q, want %q", got, want)
	}
	got, err = scope.AddExcludes([]byte("version: 1\ninclude: [/tests/**]\n"), []string{"jest.config.js"})
	if err != nil {
		t.Fatal(err)
	}
	d := mustParse(t, got)
	if got := patternStrings(d.Exclude()); !reflect.DeepEqual(got, []string{"jest.config.js"}) || !reflect.DeepEqual(patternStrings(d.Include()), []string{"/tests/**"}) {
		t.Fatalf("AddExcludes created exclude %q, include %q", got, patternStrings(d.Include()))
	}
}

func TestAddExcludesQuotesLikeAddPatternsAndKeepsComments(t *testing.T) {
	got, err := scope.AddExcludes([]byte("# Owner notes.\nversion: 1\nexclude:\n  - \"a\"\n  - \"b\"\n"), []string{"c d", "x: y"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Owner notes.\nversion: 1\nexclude:\n  - \"a\"\n  - \"b\"\n  - \"c d\"\n  - \"x: y\"\n"; string(got) != want {
		t.Fatalf("AddExcludes = %q, want %q", got, want)
	}
}

func TestAddExcludesWithNothingNewReturnsTheBytesUnchangedAndRejectsInvalidInput(t *testing.T) {
	raw := []byte("version:   1\nexclude: [a,   b]\n")
	if got, err := scope.AddExcludes(raw, []string{"b", "a"}); err != nil || !bytes.Equal(got, raw) {
		t.Errorf("all present: AddExcludes = %q, %v; want the input", got, err)
	}
	if got, err := scope.AddExcludes([]byte("version: 2\n"), []string{"x"}); !errors.Is(err, scope.ErrInvalidDeclaration) || got != nil {
		t.Errorf("wrong version: AddExcludes = %q, %v; want ErrInvalidDeclaration", got, err)
	}
	for _, pattern := range []string{"!x", "../x", `a\b`, "", "a/**b"} {
		if got, err := scope.AddExcludes([]byte("version: 1\n"), []string{pattern}); !errors.Is(err, scope.ErrInvalidPattern) || got != nil {
			t.Errorf("exclude %q: AddExcludes = %q, %v; want ErrInvalidPattern", pattern, got, err)
		}
	}
}

// A pattern that YAML reads as another scalar or as the merge key must be written quoted, so the result stays a declaration that holds it.
func TestAddPatternsAndAddExcludesQuoteWhatYAMLWouldReadAsAnotherValue(t *testing.T) {
	patterns := []string{"<<", "y", "No", "ON"}
	for name, add := range map[string]func(raw []byte) ([]byte, error){
		"include": func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, patterns, nil) },
		"runner":  func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, nil, patterns) },
		"exclude": func(raw []byte) ([]byte, error) { return scope.AddExcludes(raw, patterns) },
		"existing": func(raw []byte) ([]byte, error) {
			return scope.AddExcludes(append(raw, "exclude:\n  - a\n"...), patterns)
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := add([]byte("version: 1\n"))
			if err != nil {
				t.Fatalf("the library cannot add the patterns: %v", err)
			}
			d, err := scope.ParseDeclaration(got)
			if err != nil {
				t.Fatalf("the result is not a declaration: %v\n%s", err, got)
			}
			var held []string
			for _, list := range [][]scope.Pattern{d.Include(), d.Runner(), d.Exclude()} {
				held = append(held, patternStrings(list)...)
			}
			for _, p := range patterns {
				if !slices.Contains(held, p) {
					t.Errorf("the declaration lacks %q:\n%s", p, got)
				}
			}
		})
	}
}

// A pattern that is only a YAML 1.1 boolean is quoted on its own, without the merge key to force it, and reads back as the same string.
func TestAddPatternsQuotesAYAMLBooleanOnItsOwn(t *testing.T) {
	for _, pattern := range []string{"y", "Off"} {
		t.Run(pattern, func(t *testing.T) {
			got, err := scope.AddPatterns([]byte("version: 1\n"), []string{pattern}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want := "version: 1\ninclude:\n  - \"" + pattern + "\"\n"; string(got) != want {
				t.Errorf("AddPatterns = %q, want %q", got, want)
			}
			if d := mustParse(t, got); !reflect.DeepEqual(patternStrings(d.Include()), []string{pattern}) {
				t.Errorf("the declaration holds %q, want %q", patternStrings(d.Include()), pattern)
			}
		})
	}
}

// A declaration holds at most 1000 patterns across include, exclude and runner.
func TestParseDeclarationCapsPatternsAtOneThousand(t *testing.T) {
	declare := func(include, exclude, runner int) []byte {
		var b strings.Builder
		b.WriteString("version: 1\n")
		for key, n := range map[string]int{"include": include, "exclude": exclude, "runner": runner} {
			b.WriteString(key + ":\n")
			for i := 0; i < n; i++ {
				b.WriteString("  - " + key[:1] + strconv.Itoa(i) + "/\n")
			}
		}
		return []byte(b.String())
	}
	if _, err := scope.ParseDeclaration(declare(400, 300, 300)); err != nil {
		t.Fatalf("1000 patterns: %v", err)
	}
	for name, raw := range map[string][]byte{
		"1001 across the lists": declare(400, 300, 301),
		"1001 in one list":      declare(1001, 0, 0),
	} {
		if _, err := scope.ParseDeclaration(raw); !errors.Is(err, scope.ErrInvalidDeclaration) {
			t.Errorf("%s: error = %v, want ErrInvalidDeclaration", name, err)
		}
	}
}

// A pattern past the segment cap makes the declaration invalid, the pattern's own error kept in the chain.
func TestParseDeclarationRefusesAPatternOverTheSegmentCap(t *testing.T) {
	raw := []byte("version: 1\ninclude:\n  - /" + strings.Repeat("**/", 20000) + "x\n")
	_, err := scope.ParseDeclaration(raw)
	if !errors.Is(err, scope.ErrInvalidDeclaration) || !errors.Is(err, scope.ErrInvalidPattern) {
		t.Fatalf("error = %v, want ErrInvalidDeclaration wrapping ErrInvalidPattern", err)
	}
}

// The additions of AddPatterns and AddExcludes count toward the 1000 patterns of a declaration: a base of 999 takes
// one more and refuses two.
func TestAddPatternsAndExcludesRefuseAdditionsPastTheDeclarationCap(t *testing.T) {
	var base strings.Builder
	base.WriteString("version: 1\ninclude:\n")
	for i := 0; i < 999; i++ {
		base.WriteString("  - d" + strconv.Itoa(i) + "/\n")
	}
	raw := []byte(base.String())
	if _, err := scope.AddPatterns(raw, []string{"new-a/"}, nil); err != nil {
		t.Fatalf("AddPatterns of the 1000th pattern: %v", err)
	}
	if _, err := scope.AddPatterns(raw, []string{"new-a/"}, []string{"new-b/"}); !errors.Is(err, scope.ErrInvalidDeclaration) {
		t.Errorf("AddPatterns past the cap error = %v, want ErrInvalidDeclaration", err)
	}
	if _, err := scope.AddExcludes(raw, []string{"new-a", "new-b"}); !errors.Is(err, scope.ErrInvalidDeclaration) {
		t.Errorf("AddExcludes past the cap error = %v, want ErrInvalidDeclaration", err)
	}
}

// withProfiles is a declaration whose execution section holds the list form, body being the YAML of the list.
func withProfiles(body string) string { return withExecution("{profiles: " + body + "}") }

// refusal is a declaration that must be refused, with the valid declaration that differs from it by the one thing that
// is refused, so that a refusal cannot come from anything but that.
type refusal struct{ name, raw, valid string }

func checkRefused(t *testing.T, tests []refusal) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := scope.ParseDeclaration([]byte(tt.valid)); err != nil {
				t.Fatalf("ParseDeclaration(%q) error = %v, want the valid twin accepted", tt.valid, err)
			}
			_, err := scope.ParseDeclaration([]byte(tt.raw))
			if !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want ErrInvalidDeclaration", tt.raw, err)
			}
			if errors.Is(err, scope.ErrInvalidPattern) {
				t.Fatalf("ParseDeclaration(%q) error = %v, want no ErrInvalidPattern", tt.raw, err)
			}
		})
	}
}

func TestParseDeclarationProfilesList(t *testing.T) {
	// "+lib" sorts before "." in bytes, so only a root-first rule puts the root first; docs/examples is protected only.
	const raw = `version: 1
execution:
  profiles:
    - profile: go
      folder: tools/gen
    - profile: go
      folder: "+lib"
    - profile: go
    - profile: go
      folder: docs/examples
      run: false
    - profile: go
      folder: core
      run: true
`
	d := mustParse(t, []byte(raw))
	want := []scope.Part{{Profile: "go", Folder: "."}, {Profile: "go", Folder: "+lib"}, {Profile: "go", Folder: "core"}, {Profile: "go", Folder: "tools/gen"}}
	if got := d.Parts(); !slices.Equal(got, want) {
		t.Fatalf("Parts() = %v, want %v (root first, then by folder, the protected-only entry left out)", got, want)
	}
	// Four running Go folders are one profile: the label is go, never go-go-go-go.
	if got, ok := d.Execution(); !ok || got != (scope.Execution{Profile: "go", Canonical: "advisory", Receipt: "advisory"}) {
		t.Fatalf("Execution() = %+v, %v, want go at advisory and advisory", got, ok)
	}
	if !d.ListForm() {
		t.Fatal("ListForm() = false for the list form")
	}
}

// AddPatterns and AddExcludes rewrite the file through its YAML nodes; the list must come out as it went in.
func TestAddPatternsKeepsTheProfilesList(t *testing.T) {
	const raw = "version: 1\nexecution:\n  canonical: required\n  profiles:\n    - profile: go\n    - profile: go\n      folder: core\n      run: false\ninclude:\n  - src/**\n"
	before := mustParse(t, []byte(raw))
	for name, edit := range map[string]func() ([]byte, error){
		"AddPatterns": func() ([]byte, error) { return scope.AddPatterns([]byte(raw), []string{"docs/"}, []string{"Makefile"}) },
		"AddExcludes": func() ([]byte, error) { return scope.AddExcludes([]byte(raw), []string{"src/gen/"}) },
	} {
		edited, err := edit()
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		after := mustParse(t, edited)
		beforePolicy, _ := before.Execution()
		afterPolicy, ok := after.Execution()
		// Parts leaves the protected-only entry out, so the bytes must still hold it.
		if !ok || afterPolicy != beforePolicy || !after.ListForm() || !slices.Equal(after.Parts(), before.Parts()) || !strings.Contains(string(edited), "run: false") {
			t.Fatalf("%s changed the profiles: %+v %v %v became %+v %v %v\n%s", name, beforePolicy, before.ListForm(), before.Parts(), afterPolicy, after.ListForm(), after.Parts(), edited)
		}
	}
}

func TestParseDeclarationKeepsAFolderAsDeclared(t *testing.T) {
	for _, folder := range []string{"core", "a/b", ".hidden", "..a", "a..", "a/.b/c", "-x", "with space", "ünï", "a.b/c", strings.Repeat("a", 512)} {
		d := mustParse(t, []byte(withProfiles("[{profile: go, folder: "+strconv.Quote(folder)+"}]")))
		if got, want := d.Parts(), []scope.Part{{Profile: "go", Folder: folder}}; !slices.Equal(got, want) {
			t.Errorf("folder %q: Parts() = %v, want %v", folder, got, want)
		}
	}
}

func TestParseDeclarationLegacyProfileIsOneGoAtTheRoot(t *testing.T) {
	for name, levels := range map[string]string{"default levels": "", "named levels": ", canonical: required, receipt: before-approval"} {
		t.Run(name, func(t *testing.T) {
			scalar := mustParse(t, []byte(withExecution("{profile: go"+levels+"}")))
			list := mustParse(t, []byte(withExecution("{profiles: [{profile: go}]"+levels+"}")))
			scalarPolicy, scalarOK := scalar.Execution()
			listPolicy, listOK := list.Execution()
			if scalarPolicy != listPolicy || scalarOK != listOK || !scalarOK {
				t.Fatalf("Execution() = %+v, %v for the scalar form and %+v, %v for the list, want the same policy", scalarPolicy, scalarOK, listPolicy, listOK)
			}
			root := []scope.Part{{Profile: "go", Folder: "."}}
			if got := scalar.Parts(); !slices.Equal(got, root) {
				t.Errorf("Parts() of the scalar form = %v, want %v", got, root)
			}
			if got := list.Parts(); !slices.Equal(got, root) {
				t.Errorf("Parts() of the list = %v, want %v", got, root)
			}
			if scalar.ListForm() || !list.ListForm() {
				t.Errorf("ListForm() = %v for the scalar form and %v for the list, want false and true", scalar.ListForm(), list.ListForm())
			}
		})
	}
	if none := scope.NoDeclaration(); none.ListForm() || none.Parts() != nil {
		t.Errorf("NoDeclaration() has parts %v or the list form", none.Parts())
	}
}

func TestParseDeclarationRefusesProfileAndProfilesTogether(t *testing.T) {
	checkRefused(t, []refusal{
		{"the scalar first", withExecution("{profile: go, profiles: [{profile: go}]}"), withExecution("{profiles: [{profile: go}]}")},
		{"the list first", withExecution("{profiles: [{profile: go}], profile: go}"), withExecution("{profile: go}")},
		{"with levels and folders", withExecution("{profile: go, canonical: required, profiles: [{profile: go, folder: core}]}"), withExecution("{canonical: required, profiles: [{profile: go, folder: core}]}")},
		{"a protected-only list", withExecution("{profile: go, profiles: [{profile: go, run: false}]}"), withExecution("{profiles: [{profile: go, run: false}]}")},
		{"the list twice", withExecution("{profiles: [{profile: go}], profiles: [{profile: go}]}"), withExecution("{profiles: [{profile: go}]}")},
	})
}

func TestParseDeclarationRefusesTheSameProfileTwiceForOneFolder(t *testing.T) {
	checkRefused(t, []refusal{
		{"a folder twice", withProfiles("[{profile: go, folder: core}, {profile: go, folder: core}]"), withProfiles("[{profile: go, folder: core}, {profile: go, folder: core2}]")},
		{"an absent folder twice, which is the root twice", withProfiles("[{profile: go}, {profile: go}]"), withProfiles("[{profile: go}, {profile: go, folder: core}]")},
		{"the pair not next to its twin", withProfiles("[{profile: go, folder: a}, {profile: go, folder: b}, {profile: go, folder: a}]"), withProfiles("[{profile: go, folder: a}, {profile: go, folder: b}, {profile: go, folder: c}]")},
		{"a running entry and a protected-only one", withProfiles("[{profile: go, folder: core}, {profile: go, folder: core, run: false}]"), withProfiles("[{profile: go, folder: core}, {profile: go, folder: core2, run: false}]")},
		{"two protected-only entries", withProfiles("[{profile: go, run: false}, {profile: go, run: false}]"), withProfiles("[{profile: go, run: false}, {profile: go, folder: core, run: false}]")},
	})
}

func TestParseDeclarationNestedRunningFoldersAreAccepted(t *testing.T) {
	tests := []struct {
		name string
		list string
		want []scope.Part
	}{
		{"the root and a folder", "[{profile: go, folder: core}, {profile: go}]", []scope.Part{{Profile: "go", Folder: "."}, {Profile: "go", Folder: "core"}}},
		{"a folder and one inside it", "[{profile: go, folder: core/x}, {profile: go, folder: core}]", []scope.Part{{Profile: "go", Folder: "core"}, {Profile: "go", Folder: "core/x"}}},
		{"three levels", "[{profile: go, folder: core/x}, {profile: go}, {profile: go, folder: core}]", []scope.Part{{Profile: "go", Folder: "."}, {Profile: "go", Folder: "core"}, {Profile: "go", Folder: "core/x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := mustParse(t, []byte(withProfiles(tt.list)))
			if got := d.Parts(); !slices.Equal(got, tt.want) {
				t.Fatalf("Parts() = %v, want %v", got, tt.want)
			}
			if got, ok := d.Execution(); !ok || got.Profile != "go" {
				t.Fatalf("Execution() = %+v, %v, want the label go", got, ok)
			}
		})
	}
}

func TestParseDeclarationLevelsNeedARunningProfile(t *testing.T) {
	checkRefused(t, []refusal{
		{"a canonical level beside a protected-only entry", withExecution("{profiles: [{profile: go, run: false}], canonical: required}"), withExecution("{profiles: [{profile: go}], canonical: required}")},
		{"a receipt level beside a protected-only entry", withExecution("{profiles: [{profile: go, run: false}], receipt: off}"), withExecution("{profiles: [{profile: go}], receipt: off}")},
		{"both levels, the defaults spelled out", withExecution("{canonical: advisory, profiles: [{profile: go, run: false}], receipt: advisory}"), withExecution("{canonical: advisory, profiles: [{profile: go}], receipt: advisory}")},
		{"every entry protected only", withExecution("{profiles: [{profile: go, folder: a, run: false}, {profile: go, folder: b, run: false}], canonical: off}"), withExecution("{profiles: [{profile: go, folder: a, run: false}, {profile: go, folder: b}], canonical: off}")},
	})
	// A level beside one running entry is valid, whatever the others do.
	mixed, _ := mustParse(t, []byte(withExecution("{profiles: [{profile: go, run: false}, {profile: go, folder: core}], canonical: required}"))).Execution()
	if want := (scope.Execution{Profile: "go", Canonical: "required", Receipt: "advisory"}); mixed != want {
		t.Errorf("Execution() = %+v, want %+v", mixed, want)
	}

	// Every entry protected only, no level: valid, and nothing runs, so the policy is off and off.
	protected := mustParse(t, []byte(withProfiles("[{profile: go, run: false}, {profile: go, folder: core, run: false}]")))
	if got, ok := protected.Execution(); ok || got != (scope.Execution{}) {
		t.Fatalf("Execution() = %+v, %v, want no policy", got, ok)
	}
	if parts := protected.Parts(); parts != nil {
		t.Fatalf("Parts() = %v, want nil when nothing runs", parts)
	}
	if !protected.ListForm() {
		t.Error("ListForm() = false for a list of protected-only entries")
	}
	running := scope.NewScope(mustParse(t, []byte(withProfiles("[{profile: go}]"))))
	none := scope.NewScope(scope.NoDeclaration())
	reduced := scope.Reductions(running, scope.NewScope(protected))
	for _, want := range []string{"execution canonical: advisory -> off", "execution receipt: advisory -> off"} {
		if !slices.Contains(reduced, want) {
			t.Errorf("Reductions(go runs, all protected only) = %q, want it to hold %q", reduced, want)
		}
	}
	if got := scope.Reductions(scope.NewScope(protected), none); len(got) != 0 {
		t.Errorf("Reductions(all protected only, nothing) = %q, want none: both levels were off already", got)
	}
}

func TestProfileIDsIsTheSortedClosedSetAndACopy(t *testing.T) {
	ids := scope.ProfileIDs()
	if len(ids) == 0 || !slices.IsSorted(ids) || !slices.Contains(ids, "go") {
		t.Fatalf("ProfileIDs() = %q, want a sorted set that holds go", ids)
	}
	for _, id := range ids {
		if _, err := scope.ParseDeclaration([]byte(withProfiles("[{profile: " + id + "}]"))); err != nil {
			t.Errorf("ParseDeclaration of the profile %q of ProfileIDs() error = %v, want it accepted", id, err)
		}
	}
	ids[0] = "changed"
	if got := scope.ProfileIDs(); slices.Contains(got, "changed") {
		t.Fatalf("ProfileIDs() = %q after the caller changed a result, want a copy", got)
	}
}

// The scalar form accepts only go whatever the list accepts: a repository that runs another profile uses the list form.
func TestParseDeclarationRefusesAnUnknownProfile(t *testing.T) {
	// cpp is refused until task X18 lands its profile; that task moves its rows to the accepted ones.
	valid := withProfiles("[{profile: go}]")
	checkRefused(t, []refusal{
		{"cpp, until X18", withProfiles("[{profile: cpp}]"), valid},
		{"cpp at a folder, until X18", withProfiles("[{profile: cpp, folder: core}]"), valid},
		{"cpp beside go, until X18", withProfiles("[{profile: go}, {profile: cpp, folder: core}]"), withProfiles("[{profile: go}, {profile: go, folder: core}]")},
		{"rust", withProfiles("[{profile: rust}]"), valid},
		{"another case", withProfiles("[{profile: Go}]"), valid},
		{"an empty string", withProfiles(`[{profile: ""}]`), valid},
		{"an integer", withProfiles("[{profile: 1}]"), valid},
		{"a boolean", withProfiles("[{profile: true}]"), valid},
		{"null", withProfiles("[{profile: ~}]"), valid},
		{"a sequence", withProfiles("[{profile: [go]}]"), valid},
		{"a mapping", withProfiles("[{profile: {a: go}}]"), valid},
		{"an unknown one after a known one", withProfiles("[{profile: go}, {profile: rust, folder: core}]"), withProfiles("[{profile: go}, {profile: go, folder: core}]")},
		{"the scalar form takes go only: cpp", withExecution("{profile: cpp}"), withExecution("{profile: go}")},
		{"the scalar form takes go only: rust", withExecution("{profile: rust}"), withExecution("{profile: go}")},
	})
}

func TestParseDeclarationRefusesABadProfilesEntry(t *testing.T) {
	entry := func(body string) string { return withProfiles("[" + body + "]") }
	many := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = "{profile: go, folder: f" + strconv.Itoa(i) + "}"
		}
		return withProfiles("[" + strings.Join(items, ", ") + "]")
	}
	listed, folder, run := entry("{profile: go}"), entry("{profile: go, folder: core}"), entry("{profile: go, run: false}")
	checkRefused(t, []refusal{
		// The list itself.
		{"an empty list", withProfiles("[]"), listed},
		{"a null list", withProfiles("~"), listed},
		{"a list with nothing after its key", "version: 1\nexecution:\n  profiles:\n", listed},
		{"a scalar", withProfiles("go"), listed},
		{"a mapping", withProfiles("{profile: go}"), listed},
		{"nine entries", many(9), many(8)},
		{"a hundred entries", many(100), many(8)},
		// An entry.
		{"an entry that is a string", entry("go"), listed},
		{"an entry that is a sequence", entry("[go]"), listed},
		{"a null entry", entry("~"), listed},
		{"an empty entry", entry("{}"), listed},
		{"an entry without a profile", entry("{folder: core}"), folder},
		{"an entry with only run", entry("{run: false}"), run},
		{"an unknown key", entry("{profile: go, extra: x}"), listed},
		{"a misspelt key", entry("{profile: go, Folder: core}"), folder},
		{"the old canonical key", entry("{profile: go, canonical: off}"), listed},
		{"a key that is not a string", entry("{profile: go, 1: x}"), listed},
		{"a merge key", entry("{<<: {profile: go}}"), listed},
		{"profile twice", entry("{profile: go, profile: go}"), listed},
		{"folder twice", entry("{profile: go, folder: a, folder: a}"), folder},
		{"run twice", entry("{profile: go, run: true, run: true}"), run},
		{"an alias is not followed", entry("{profile: &p go, folder: *p}"), entry("{profile: &p go, folder: core}")},
		// A folder.
		{"the root spelled out", entry("{profile: go, folder: .}"), folder},
		{"a leading slash", entry("{profile: go, folder: /a}"), folder},
		{"a trailing slash", entry("{profile: go, folder: a/}"), folder},
		{"an empty segment", entry("{profile: go, folder: a//b}"), folder},
		{"a dot segment", entry("{profile: go, folder: a/./b}"), folder},
		{"a leading dot segment", entry("{profile: go, folder: ./a}"), folder},
		{"a dot-dot segment", entry("{profile: go, folder: a/../b}"), folder},
		{"a leading dot-dot segment", entry("{profile: go, folder: ../a}"), folder},
		{"dot-dot", entry("{profile: go, folder: ..}"), folder},
		{"an empty folder", entry(`{profile: go, folder: ""}`), folder},
		{"a folder of 513 bytes", entry("{profile: go, folder: " + strconv.Quote(strings.Repeat("a", 513)) + "}"), entry("{profile: go, folder: " + strconv.Quote(strings.Repeat("a", 512)) + "}")},
		{"a folder of 514 bytes in two-byte characters", entry("{profile: go, folder: " + strconv.Quote(strings.Repeat("é", 257)) + "}"), entry("{profile: go, folder: " + strconv.Quote(strings.Repeat("é", 256)) + "}")},
		{"a tab", entry(`{profile: go, folder: "a\tb"}`), folder},
		{"a NUL", entry(`{profile: go, folder: "a\0b"}`), folder},
		{"a DEL", entry(`{profile: go, folder: "a\x7fb"}`), folder},
		{"a C1 control", entry(`{profile: go, folder: "a\u0085b"}`), folder},
		{"a zero-width space", entry(`{profile: go, folder: "a\u200Bb"}`), folder},
		{"a right-to-left override", entry(`{profile: go, folder: "a\u202Eb"}`), folder},
		{"a byte order mark", entry(`{profile: go, folder: "a\uFEFFb"}`), folder},
		{"a byte that is not UTF-8", "version: 1\nexecution:\n  profiles:\n    - profile: go\n      folder: \"a\xffb\"\n", folder},
		{"an integer folder", entry("{profile: go, folder: 2024}"), folder},
		{"a null folder", entry("{profile: go, folder: ~}"), folder},
		{"a sequence folder", entry("{profile: go, folder: [a]}"), folder},
		{"a mapping folder", entry("{profile: go, folder: {a: b}}"), folder},
		// run.
		{"run as a quoted string", entry(`{profile: go, run: "false"}`), run},
		{"run as yes, which YAML 1.2 reads as a string", entry("{profile: go, run: yes}"), run},
		{"run as no", entry("{profile: go, run: no}"), run},
		{"run as on", entry("{profile: go, run: on}"), run},
		{"run as an integer", entry("{profile: go, run: 1}"), run},
		{"run as null", entry("{profile: go, run: ~}"), run},
		{"run as a sequence", entry("{profile: go, run: [true]}"), run},
		{"run as a mapping", entry("{profile: go, run: {a: b}}"), run},
	})
}
