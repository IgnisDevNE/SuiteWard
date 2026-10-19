package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A declaration is shown in a fence to copy, and a fence shows no escape: a character that is not graphic (a bidi control, a
// zero-width space, a NUL, a carriage return) would reach the owner's clipboard unseen. Only a newline and a tab are accepted, so
// the caller falls back to the pattern alone, as it does for a declaration that is too large.

// declarationNotices builds each notice with the declaration bytes d in the field that the comment shows.
func declarationNotices(d []byte) map[string]func() error {
	return map[string]func() error{
		"a runner-uncovered protect declaration": func() error {
			n := runnerNotice()
			n.Protect = d
			return commandtext.ValidateRunnerUncovered(n)
		},
		"a runner-uncovered ignore declaration": func() error {
			n := runnerNotice()
			n.Ignore = d
			return commandtext.ValidateRunnerUncovered(n)
		},
		"a scope notice declaration": func() error {
			n := notice()
			n.Declaration = d
			return commandtext.ValidateScopeNotice(n)
		},
		"an invalid-source declaration": func() error {
			return commandtext.ValidateInvalidSource(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "l", Declaration: d})
		},
	}
}

func TestADeclarationWithAHiddenCharacterIsRefused(t *testing.T) {
	for name, text := range map[string]string{
		"a bidi override":    "version: 1\n# \u202e gnp\n",
		"a zero-width space": "version: 1\n# a\u200bb\n",
		"a NUL":              "version: 1\n# a\x00b\n",
		"a carriage return":  "version: 1\r\n",
		"an escape":          "version: 1\n# \x1b[2J\n",
		"a next line":        "version: 1\n# a\u0085b\n",
		"a byte order mark":  "\ufeffversion: 1\n",
		"a line separator":   "version: 1\n# a\u2028b\n",
		"a bidi isolate":     "version: 1\n# \u2066x\n",
	} {
		for what, validate := range declarationNotices([]byte(text)) {
			t.Run(name+" in "+what, func(t *testing.T) {
				if err := validate(); err == nil {
					t.Errorf("the validator accepts %q", text)
				}
			})
		}
	}
}

func TestADeclarationMayHoldNewlinesTabsAndGraphicCharacters(t *testing.T) {
	text := "version: 1\ninclude:\n\t- \"caf\u00e9/**\" # \u65e5\u672c\u8a9e \u20ac\n"
	for what, validate := range declarationNotices([]byte(text)) {
		t.Run(what, func(t *testing.T) {
			if err := validate(); err != nil {
				t.Errorf("the validator refuses %q: %v", text, err)
			}
		})
	}
}

func TestAHiddenCharacterInADeclarationPanicsTheCommentsAndNamesNoValue(t *testing.T) {
	d := []byte("version: 1\n# \u202e\n")
	n := notice()
	n.Declaration = d
	mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
	r := runnerNotice()
	r.Protect = d
	mustPanic(t, func() { commandtext.RunnerUncoveredComment(r) })
	s := commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "l", Declaration: d}
	mustPanic(t, func() { commandtext.InvalidSourceComment(s) })
	if err := commandtext.ValidateInvalidSource(s); err == nil || strings.Contains(err.Error(), "\u202e") {
		t.Errorf("the error is %v, want one that does not repeat the declaration", err)
	}
}
