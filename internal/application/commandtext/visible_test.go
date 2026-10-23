package commandtext_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// An escape that Visible writes must not be a text that a file can hold: a literal backslash is written twice, so that \u202e is a hidden
// character and \\u202e is the six characters of a file that spells it.
func TestVisibleWritesALiteralBackslashTwiceSoThatEscapesAreDistinguishable(t *testing.T) {
	for in, want := range map[string]string{
		`C:\dir\file`:           `C:\\dir\\file`,
		`\u202e`:                `\\u202e`,
		"a\u202eb":              `a\u202eb`,
		`\` + "\u202e" + `\x00`: `\\\u202e\\x00`,
		"a\x00b\\":              `a\x00b\\`,
	} {
		if got := commandtext.Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
}
