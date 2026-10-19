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
		"a bidi override":                   "version: 1\n# \u202e gnp\n",
		"a zero-width space":                "version: 1\n# a\u200bb\n",
		"a NUL":                             "version: 1\n# a\x00b\n",
		"a lone carriage return":            "version: 1\r# x\n",
		"a carriage return ending the text": "version: 1\r",
		"a carriage return before another":  "version: 1\r\r\n",
		"an escape":                         "version: 1\n# \x1b[2J\n",
		"a next line":                       "version: 1\n# a\u0085b\n",
		"a byte order mark":                 "\ufeffversion: 1\n",
		"a line separator":                  "version: 1\n# a\u2028b\n",
		"a bidi isolate":                    "version: 1\n# \u2066x\n",
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

// A CRLF line ending is the normal form of a file written on Windows, not a hidden character; only a carriage return that no line feed follows is.
func TestADeclarationMayHoldCRLFLineEndings(t *testing.T) {
	text := "version: 1\r\ninclude:\r\n  - /tests/**\r\n"
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

// The pattern that accepts a link is shown to be pasted into exclude. A path with a character that is not graphic is shown escaped,
// which is not the pattern, so the comment offers the pattern alone only for a path shown as it is; the head's own declaration is
// exact whatever the path, so an offer that holds it stays.
func TestInvalidSourceCommentDoesNotOfferAPatternForAPathThatIsShownEscaped(t *testing.T) {
	const declaration = "version: 1\nexclude:\n  - /some_link\n"
	for _, path := range []string{"a\u202eb", "a\nb", "a\xffb", "a\u200bb"} {
		without := render(t, func() string {
			return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: path})
		})
		if strings.Contains(without, "accept") || !strings.Contains(without, "remove the link or point it to a regular file, and push") {
			t.Errorf("the comment for %q offers the pattern or lacks the other step:\n%s", path, without)
		}
		with := render(t, func() string {
			return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: path, Declaration: []byte(declaration)})
		})
		if !strings.Contains(with, "accept the link") || !strings.Contains(with, "```\n"+declaration+"```") {
			t.Errorf("the comment for %q with a declaration lacks the offer:\n%s", path, with)
		}
	}
}
