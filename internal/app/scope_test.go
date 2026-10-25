package app

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

const declarationFile = ".suiteward.yml"

// A declaration like the ones people write: comments, a blank line, a list whose items are not all quoted the same way.
const commentedDeclaration = `version: 1
# test files
include:
  - "**/*_test.go"   # go tests
  - scripts/test-*.ps1

# runner files
runner:
  - scripts/ci.ps1
`

type scopeResult struct {
	code           int
	stdout, stderr string
	file           string // the declaration afterwards; empty when there is none
	others         []string
}

// scopeCommand runs `suiteward <args>` in a fresh checkout directory. A non-empty declaration is the .suiteward.yml that it holds.
func scopeCommand(t *testing.T, declaration string, args ...string) scopeResult {
	t.Helper()
	dir := t.TempDir()
	if declaration != "" {
		if err := os.WriteFile(dir+"/"+declarationFile, []byte(declaration), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	var r scopeResult
	r.code, r.stdout, r.stderr = run(args, Env{})
	if raw, err := os.ReadFile(declarationFile); err == nil {
		r.file = string(raw)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != declarationFile {
			r.others = append(r.others, e.Name())
		}
	}
	return r
}

func scopeAdd(t *testing.T, declaration string, args ...string) scopeResult {
	t.Helper()
	return scopeCommand(t, declaration, append([]string{"scope", "add"}, args...)...)
}

// onlyInserted reports whether updated is original with one run of bytes inserted somewhere and nothing else changed.
func onlyInserted(original, updated string) bool {
	if len(updated) < len(original) {
		return false
	}
	p := 0
	for p < len(original) && original[p] == updated[p] {
		p++
	}
	n := len(updated) - len(original)
	return updated[:p]+updated[p+n:] == original
}

// declared is what the domain parser reads from a declaration, as the strings of its three lists.
func declared(t *testing.T, raw string) map[string][]string {
	t.Helper()
	d, err := scope.ParseDeclaration([]byte(raw))
	if err != nil {
		t.Fatalf("the domain parser refuses the declaration: %v\n%s", err, raw)
	}
	names := func(patterns []scope.Pattern) []string {
		out := make([]string, len(patterns))
		for i, p := range patterns {
			out[i] = p.String()
		}
		return out
	}
	return map[string][]string{"include": names(d.Include()), "exclude": names(d.Exclude()), "runner": names(d.Runner())}
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestScopeAddWritesOnlyTheNewLines(t *testing.T) {
	for _, tc := range []struct {
		name, old string
		args      []string
		list      string
		added     []string
		want      string
	}{
		{
			name: "after the last item, comments and blank lines kept; plain, as the items differ",
			old:  commentedDeclaration, args: []string{"docs/spec/"}, list: "include", added: []string{"docs/spec/"},
			want: strings.Replace(commentedDeclaration, "  - scripts/test-*.ps1\n", "  - scripts/test-*.ps1\n  - docs/spec/\n", 1),
		},
		{
			name: "the runner list with --runner",
			old:  commentedDeclaration, args: []string{"--runner", "scripts/ci2.ps1"}, list: "runner", added: []string{"scripts/ci2.ps1"},
			want: commentedDeclaration + "  - scripts/ci2.ps1\n",
		},
		{
			name: "a list whose items are all double-quoted",
			old:  "version: 1\ninclude:\n  - \"a_test.go\"\n  - \"b/\"\nrunner:\n  - r\n", args: []string{"**/*.snap"}, list: "include", added: []string{"**/*.snap"},
			want: "version: 1\ninclude:\n  - \"a_test.go\"\n  - \"b/\"\n  - \"**/*.snap\"\nrunner:\n  - r\n",
		},
		{
			name: "a list whose items are all single-quoted, a pattern with an apostrophe",
			old:  "version: 1\ninclude:\n  - 'a'\n  - 'b'\n", args: []string{"it's/"}, list: "include", added: []string{"it's/"},
			want: "version: 1\ninclude:\n  - 'a'\n  - 'b'\n  - 'it''s/'\n",
		},
		{
			name: "a plain list, a pattern that YAML would read as an alias",
			old:  "version: 1\ninclude:\n  - a\n  - b\n", args: []string{"*.golden"}, list: "include", added: []string{"*.golden"},
			want: "version: 1\ninclude:\n  - a\n  - b\n  - '*.golden'\n",
		},
		{
			name: "a plain list, a pattern that YAML 1.1 reads as a boolean",
			old:  "version: 1\ninclude:\n  - a\n", args: []string{"yes"}, list: "include", added: []string{"yes"},
			want: "version: 1\ninclude:\n  - a\n  - \"yes\"\n",
		},
		{
			name: "several patterns, in the order given",
			old:  "version: 1\ninclude:\n  - a\n", args: []string{"x/", "y/"}, list: "include", added: []string{"x/", "y/"},
			want: "version: 1\ninclude:\n  - a\n  - x/\n  - y/\n",
		},
		{
			name: "CRLF line endings",
			old:  crlf(commentedDeclaration), args: []string{"docs/spec/"}, list: "include", added: []string{"docs/spec/"},
			want: strings.Replace(crlf(commentedDeclaration), "  - scripts/test-*.ps1\r\n", "  - scripts/test-*.ps1\r\n  - docs/spec/\r\n", 1),
		},
		{
			name: "no final newline stays so",
			old:  "version: 1\ninclude:\n  - a", args: []string{"b/"}, list: "include", added: []string{"b/"},
			want: "version: 1\ninclude:\n  - a\n  - b/",
		},
		{
			name: "no final newline in a CRLF file",
			old:  "version: 1\r\ninclude:\r\n  - a", args: []string{"b/"}, list: "include", added: []string{"b/"},
			want: "version: 1\r\ninclude:\r\n  - a\r\n  - b/",
		},
		{
			name: "dashes at the column of their key",
			old:  "version: 1\ninclude:\n- a\n- b\nrunner:\n- c\n", args: []string{"d/"}, list: "include", added: []string{"d/"},
			want: "version: 1\ninclude:\n- a\n- b\n- d/\nrunner:\n- c\n",
		},
		{
			name: "four spaces and a wide separator",
			old:  "version: 1\ninclude:\n    -   a\n    -   b\n", args: []string{"c/"}, list: "include", added: []string{"c/"},
			want: "version: 1\ninclude:\n    -   a\n    -   b\n    -   c/\n",
		},
		{
			name: "a comment under the list stays under the new line",
			old:  "version: 1\ninclude:\n  - a\n  # about the runner\nrunner:\n  - r\n", args: []string{"b/"}, list: "include", added: []string{"b/"},
			want: "version: 1\ninclude:\n  - a\n  - b/\n  # about the runner\nrunner:\n  - r\n",
		},
		{
			name: "a document that starts with a marker",
			old:  "---\nversion: 1\ninclude:\n  - a\n", args: []string{"b/"}, list: "include", added: []string{"b/"},
			want: "---\nversion: 1\ninclude:\n  - a\n  - b/\n",
		},
		{
			name: "a pattern that another list holds is not a duplicate",
			old:  commentedDeclaration, args: []string{"scripts/ci.ps1"}, list: "include", added: []string{"scripts/ci.ps1"},
			want: strings.Replace(commentedDeclaration, "  - scripts/test-*.ps1\n", "  - scripts/test-*.ps1\n  - scripts/ci.ps1\n", 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := scopeAdd(t, tc.old, tc.args...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr %q; want 0 and nothing on stderr", r.code, r.stderr)
			}
			if r.file != tc.want {
				t.Errorf("the declaration is\n%q\nwant\n%q", r.file, tc.want)
			}
			if !onlyInserted(tc.old, r.file) {
				t.Errorf("more than inserted lines changed:\n%q\n%q", tc.old, r.file)
			}
			before, after := declared(t, tc.old), declared(t, r.file)
			before[tc.list] = append(before[tc.list], tc.added...)
			for name := range before {
				if !slices.Equal(before[name], after[name]) {
					t.Errorf("the %s list reads %q; want %q", name, after[name], before[name])
				}
			}
			if len(r.others) != 0 {
				t.Errorf("the command left %q in the directory", r.others)
			}
		})
	}
}

func TestScopeAddPrintsTheLinesItAddedAndWhatItTakesToProtectThem(t *testing.T) {
	r := scopeAdd(t, commentedDeclaration, "docs/spec/", "**/*.snap")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "  - docs/spec/" || lines[1] != "  - '**/*.snap'" {
		t.Fatalf("stdout is\n%s\nwant the two added lines, then one line about the approval", r.stdout)
	}
	for _, word := range []string{"protect", "owner", "approves", "pull request"} {
		if !strings.Contains(lines[2], word) {
			t.Errorf("the last line %q lacks %q", lines[2], word)
		}
	}
}

func TestScopeAddResultCoversTheNewPatterns(t *testing.T) {
	r := scopeAdd(t, commentedDeclaration, "docs/spec/**", "internal/*.md")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	d, err := scope.ParseDeclaration([]byte(r.file))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{"docs/spec/a/b.md": true, "internal/README.md": true, "internal/a/README.md": false, "docs/other.md": false} {
		covered := false
		for _, p := range d.Include() {
			if ok, err := p.Match(path); err != nil {
				t.Fatal(err)
			} else if ok {
				covered = true
			}
		}
		if covered != want {
			t.Errorf("include covers %q = %v; want %v", path, covered, want)
		}
	}
}

func TestScopeAddSkipsWhatTheListHolds(t *testing.T) {
	t.Run("everything is held: nothing is written", func(t *testing.T) {
		r := scopeAdd(t, commentedDeclaration, "**/*_test.go", "scripts/test-*.ps1")
		if r.code != 0 || r.file != commentedDeclaration {
			t.Fatalf("exit %d, the declaration is\n%q", r.code, r.file)
		}
		for _, held := range []string{"**/*_test.go", "scripts/test-*.ps1", "already"} {
			if !strings.Contains(r.stdout, held) {
				t.Errorf("stdout does not report %q:\n%s", held, r.stdout)
			}
		}
	})
	t.Run("some are held", func(t *testing.T) {
		r := scopeAdd(t, commentedDeclaration, "**/*_test.go", "docs/")
		want := strings.Replace(commentedDeclaration, "  - scripts/test-*.ps1\n", "  - scripts/test-*.ps1\n  - docs/\n", 1)
		if r.code != 0 || r.file != want {
			t.Fatalf("exit %d, the declaration is\n%q\nwant\n%q", r.code, r.file, want)
		}
		if !strings.Contains(r.stdout, "**/*_test.go") || !strings.Contains(r.stdout, "already") {
			t.Errorf("stdout does not report the held pattern:\n%s", r.stdout)
		}
	})
	t.Run("a pattern given twice is added once", func(t *testing.T) {
		r := scopeAdd(t, commentedDeclaration, "docs/", "docs/")
		if want := strings.Replace(commentedDeclaration, "  - scripts/test-*.ps1\n", "  - scripts/test-*.ps1\n  - docs/\n", 1); r.code != 0 || r.file != want {
			t.Fatalf("exit %d, the declaration is\n%q", r.code, r.file)
		}
	})
}

func TestScopeAddRefusesAnInvalidPatternAndWritesNothing(t *testing.T) {
	for name, pattern := range map[string]string{
		"a zero-width space":       "a\u200b/",
		"a Hangul filler":          "a\u3164/",
		"a bidirectional control":  "a\u202e/",
		"a variation selector":     "a\ufe0f/",
		"an invalid UTF-8 byte":    "a\xffb/",
		"a tab":                    "a\tb/",
		"a line feed":              "a\nb/",
		"too many segments":        strings.Repeat("a/", scope.MaxPatternSegments+1),
		"a negation":               "!a/",
		"a backslash":              `a\b/`,
		"a parent segment":         "a/../b",
		"** with other characters": "a/**b",
		"blank":                    "  ",
		"empty":                    "",
	} {
		t.Run(name, func(t *testing.T) {
			r := scopeAdd(t, commentedDeclaration, "good/", pattern)
			if r.code != 1 || r.stdout != "" || r.file != commentedDeclaration {
				t.Fatalf("exit %d, stdout %q, the declaration is\n%q; want 1, nothing on stdout and no change", r.code, r.stdout, r.file)
			}
			if r.stderr == "" {
				t.Error("nothing explains the refusal")
			}
			if strings.ContainsAny(r.stderr, "\u200b\u3164\u202e\ufe0f\xff") {
				t.Errorf("the refusal shows a character that cannot be seen:\n%q", r.stderr)
			}
			if len(r.others) != 0 {
				t.Errorf("the command left %q in the directory", r.others)
			}
		})
	}
}

func TestScopeAddLeavesWhatItCannotInsertSafelyToTheUser(t *testing.T) {
	for _, tc := range []struct {
		name, declaration string
		args              []string
		list              string
	}{
		{"flow style", "version: 1\ninclude: [a, b]\n", nil, "include"},
		{"the list is absent", "version: 1\nrunner:\n  - r\n", nil, "include"},
		{"the runner list is absent", "version: 1\ninclude:\n  - a\n", []string{"--runner"}, "runner"},
		{"a key with no list", "version: 1\ninclude:\nrunner:\n  - r\n", nil, "include"},
		{"the key appears twice", "version: 1\ninclude:\n  - a\ninclude:\n  - b\n", nil, "include"},
		{"an anchor on the list", "version: 1\ninclude: &tests\n  - a\n", nil, "include"},
		{"a tag on the list", "version: 1\ninclude: !!seq\n  - a\n", nil, "include"},
		{"a tab after the dash", "version: 1\ninclude:\n  -\ta\n", nil, "include"},
		{"a tab in the indentation", "version: 1\ninclude:\n\t- a\n", nil, "include"},
		{"an item on several lines", "version: 1\ninclude:\n  - a\n    b\n", nil, "include"},
		{"a quoted item on several lines", "version: 1\ninclude:\n  - \"a\n    b\"\n", nil, "include"},
		{"an item on the line below its dash", "version: 1\ninclude:\n  -\n    a\n", nil, "include"},
		{"a block scalar item", "version: 1\ninclude:\n  - |\n    a\n", nil, "include"},
		{"a declaration the domain refuses", "version: 2\ninclude:\n  - a\n", nil, "include"},
		{"an unknown key", "version: 1\nunknown: 1\ninclude:\n  - a\n", nil, "include"},
		{"not YAML", "include: [a\n", nil, "include"},
		{"a line break that only YAML counts", "# note\u0085\nversion: 1\ninclude:\n  - a\n", nil, "include"},
		{"a lone carriage return that only YAML counts", "# note\r# more\nversion: 1\ninclude:\n  - a\n", nil, "include"},
		{
			// YAML counts both breaks, so it reports the include item on the line where the runner item is: inserting there would widen the other list.
			"line breaks that would put the item into the other list",
			"# note\u0085\u0085\ninclude:\n  - a\nrunner:\n  - a\nversion: 1\n", nil, "include",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := scopeAdd(t, tc.declaration, append(tc.args, "added/")...)
			if r.code != 1 || r.stdout != "" || r.file != tc.declaration {
				t.Fatalf("exit %d, stdout %q, the declaration is\n%q; want 1, nothing on stdout and no change", r.code, r.stdout, r.file)
			}
			if want := tc.list + ":\n  - added/\n"; !strings.Contains(r.stderr, want) {
				t.Errorf("stderr does not give the lines to add by hand (%q):\n%s", want, r.stderr)
			}
			if len(r.others) != 0 {
				t.Errorf("the command left %q in the directory", r.others)
			}
		})
	}
}

// The lines for the user are the ones that the list lacks: a pattern it holds, pasted, would make the declaration invalid.
func TestScopeAddOffersByHandOnlyWhatTheListLacks(t *testing.T) {
	r := scopeAdd(t, "version: 1\ninclude: [a, b]\n", "a", "c/")
	if r.code != 1 || !strings.Contains(r.stderr, "include:\n  - c/\n") || strings.Contains(r.stderr, "  - a\n") {
		t.Fatalf("exit %d, stderr:\n%s", r.code, r.stderr)
	}
}

func TestScopeAddNeedsADeclaration(t *testing.T) {
	r := scopeAdd(t, "", "docs/")
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, declarationFile) || r.file != "" || len(r.others) != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q, file %q, others %q; want 1, a message that names %s and nothing created", r.code, r.stdout, r.stderr, r.file, r.others, declarationFile)
	}
}

func TestScopeAddDoesNotFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/real.yml", []byte(commentedDeclaration), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.yml", dir+"/"+declarationFile); err != nil {
		t.Skipf("this system does not allow a symlink: %v", err)
	}
	t.Chdir(dir)
	code, _, stderr := run([]string{"scope", "add", "docs/"}, Env{})
	linked, err := os.Lstat(declarationFile)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile("real.yml")
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || linked.Mode()&os.ModeSymlink == 0 || string(target) != commentedDeclaration || stderr == "" {
		t.Errorf("exit %d, stderr %q, the link is still a link: %v; want 1, a message and nothing changed", code, stderr, linked.Mode()&os.ModeSymlink != 0)
	}
}

// A declaration holds at most scope.MaxDeclarationPatterns patterns, which the parser enforces: the command says so instead of printing lines
// that would make the declaration invalid.
func TestScopeAddStopsAtTheCapOfADeclaration(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\ninclude:\n")
	for i := range scope.MaxDeclarationPatterns - 1 {
		fmt.Fprintf(&b, "  - p%d/\n", i)
	}
	full := b.String()

	r := scopeAdd(t, full, "last/")
	if r.code != 0 || len(declared(t, r.file)["include"]) != scope.MaxDeclarationPatterns {
		t.Errorf("one more pattern: exit %d, stderr %q", r.code, r.stderr)
	}
	r = scopeAdd(t, full, "one/", "two/")
	if r.code != 1 || r.file != full || r.stdout != "" || !strings.Contains(r.stderr, fmt.Sprint(scope.MaxDeclarationPatterns)) {
		t.Errorf("two more patterns: exit %d, stdout %q, stderr %q, the declaration changed: %v", r.code, r.stdout, r.stderr, r.file != full)
	}
	if strings.Contains(r.stderr, "  - one/") {
		t.Errorf("the refusal offers lines that would pass the cap:\n%s", r.stderr)
	}
}

func TestScopeUsageErrorsExitWithTwoAndChangeNothing(t *testing.T) {
	for _, args := range [][]string{
		{"scope"},
		{"scope", "remove", "docs/"},
		{"scope", "add"},
		{"scope", "add", "--runner"},
		{"scope", "add", "--no-such-flag", "docs/"},
		{"scope", "add", "docs/", "--runner"}, // a flag after a pattern would be added as a pattern
		{"scope", "add", "-docs/"},
	} {
		r := scopeCommand(t, commentedDeclaration, args...)
		if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "usage") || r.file != commentedDeclaration {
			t.Errorf("Run(%q) = %d, stdout %q, stderr %q, the declaration changed: %v; want 2, nothing on stdout, a usage message and no change", args, r.code, r.stdout, r.stderr, r.file != commentedDeclaration)
		}
	}
}

func TestUsageNamesScopeAdd(t *testing.T) {
	_, _, stderr := run(nil, Env{})
	if !strings.Contains(stderr, "suiteward scope add [--runner] PATTERN...") {
		t.Errorf("the usage lacks the scope add command:\n%s", stderr)
	}
}
