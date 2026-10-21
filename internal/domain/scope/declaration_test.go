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
