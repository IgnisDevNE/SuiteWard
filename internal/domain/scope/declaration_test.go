package scope_test

import (
	"bytes"
	"errors"
	"reflect"
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
