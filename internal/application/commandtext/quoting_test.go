package commandtext_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// A review suggestion extends a list of the head's .suiteward.yml, so its items are written as scope.AddPatterns writes them
// (docs/contracts/runtime.md, "The added list items follow the quoting style of the list's existing items"): in the quoting that
// the items of the list share, and in the plain style when the block creates the list, quoted as YAML needs.

var quotingPatterns = []string{"**/testdata/**", "it's/x", "*_test.go", "pytest.ini", "x: y/**", "true", "a/b", "<<", "y", "Off"}

// addedLines are the lines that AddPatterns (or AddExcludes) writes after anchor in file, without the lines of file.
func addedLines(t *testing.T, file string, add func([]byte) ([]byte, error), anchor string) string {
	t.Helper()
	out, err := add([]byte(file))
	if err != nil {
		t.Fatalf("the library cannot add the patterns: %v", err)
	}
	_, after, found := strings.Cut(string(out), anchor+"\n")
	if !found {
		t.Fatalf("the anchor %q is not in\n%s", anchor, out)
	}
	return strings.TrimSuffix(after, "\n")
}

func TestScopeSuggestionBodyQuotesTheItemsAsTheListDoes(t *testing.T) {
	for name, anchor := range map[string]string{"double quoted": `  - "a/**"`, "single quoted": `  - 'a/**'`, "plain": "  - a/**"} {
		t.Run(name, func(t *testing.T) {
			file := "version: 1\ninclude:\n" + anchor + "\n"
			want := addedLines(t, file, func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, quotingPatterns, nil) }, anchor)
			block := suggestionBlock.FindStringSubmatch(suggestionBody(t, anchor, "  ", quotingPatterns))
			if len(block) < 2 || block[1] != anchor+"\n"+want {
				t.Errorf("the block is %q, want the anchor and\n%s", block, want)
			}
		})
	}
}

func TestRunnerChoiceBodyQuotesTheItemsAsTheListDoes(t *testing.T) {
	const include = "  - /tests/**"
	for name, c := range map[string]struct {
		choice  commandtext.RunnerChoice
		newList string
		add     func([]byte) ([]byte, error)
	}{
		"a created runner list":  {commandtext.ProtectChoice, "runner", func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, nil, quotingPatterns) }},
		"a created exclude list": {commandtext.IgnoreChoice, "exclude", func(raw []byte) ([]byte, error) { return scope.AddExcludes(raw, quotingPatterns) }},
	} {
		t.Run(name, func(t *testing.T) {
			file := "version: 1\ninclude:\n" + include + "\n"
			want := addedLines(t, file, c.add, include)
			block := suggestionBlock.FindStringSubmatch(choiceBody(t, c.choice, include, "", quotingPatterns, c.newList))
			if len(block) < 2 || block[1] != include+"\n"+want {
				t.Errorf("the block is %q, want the anchor and\n%s", block, want)
			}
		})
	}
	for name, anchor := range map[string]string{"double quoted": `  - "a/**"`, "single quoted": `  - 'a/**'`, "plain": "  - a/**"} {
		t.Run("an existing "+name+" list", func(t *testing.T) {
			file := "version: 1\nrunner:\n" + anchor + "\n"
			want := addedLines(t, file, func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, nil, quotingPatterns) }, anchor)
			block := suggestionBlock.FindStringSubmatch(choiceBody(t, commandtext.ProtectChoice, anchor, "  ", quotingPatterns, ""))
			if len(block) < 2 || block[1] != anchor+"\n"+want {
				t.Errorf("the block is %q, want the anchor and\n%s", block, want)
			}
		})
	}
}

// A suggestion that is committed must leave a declaration that holds each pattern as written, whatever YAML would read the plain item as
// (the merge key `<<` among them).
func TestSuggestionItemsStayStringsWhenCommitted(t *testing.T) {
	patterns := []string{"<<", "y", "Off", "true"}
	include := suggestionBlock.FindStringSubmatch(suggestionBody(t, "  - a/**", "  ", patterns))
	protect := suggestionBlock.FindStringSubmatch(choiceBody(t, commandtext.ProtectChoice, "  - a/**", "  ", patterns, ""))
	ignore := suggestionBlock.FindStringSubmatch(choiceBody(t, commandtext.IgnoreChoice, "  - a/**", "  ", patterns, ""))
	for name, c := range map[string]struct {
		block []string
		key   string
		list  func(scope.Declaration) []scope.Pattern
	}{
		"include": {include, "include", scope.Declaration.Include},
		"runner":  {protect, "runner", scope.Declaration.Runner},
		"exclude": {ignore, "exclude", scope.Declaration.Exclude},
	} {
		t.Run(name, func(t *testing.T) {
			if len(c.block) < 2 {
				t.Fatal("no suggestion block")
			}
			d, err := scope.ParseDeclaration([]byte("version: 1\n" + c.key + ":\n" + c.block[1] + "\n"))
			if err != nil {
				t.Fatalf("the committed suggestion is not a declaration: %v\n%s", err, c.block[1])
			}
			var got []string
			for _, p := range c.list(d) {
				got = append(got, p.String())
			}
			if want := append([]string{"a/**"}, patterns...); !slices.Equal(got, want) {
				t.Errorf("the declaration holds %q, want %q\n%s", got, want, c.block[1])
			}
		})
	}
}
