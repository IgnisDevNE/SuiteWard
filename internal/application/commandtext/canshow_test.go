package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

func TestCanShowDecidesWhatADeclarationMayBe(t *testing.T) {
	for name, tc := range map[string]struct {
		declaration string
		want        bool
	}{
		"nothing":                  {"", true},
		"exactly the limit":        {strings.Repeat("a", commandtext.MaxDeclaration), true},
		"one byte over the limit":  {strings.Repeat("a", commandtext.MaxDeclaration+1), false},
		"CRLF line endings":        {"include:\r\n  - a\r\n", true},
		"a tab":                    {"include:\n\t- a\n", true},
		"a lone carriage return":   {"include:\r  - a\n", false},
		"a zero-width space":       {"include:\n  - a\u200b\n", false},
		"a bidi control":           {"include:\n  - a\u202e\n", false},
		"bytes that are not UTF-8": {"include:\n  - \xff\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := commandtext.CanShow([]byte(tc.declaration)); got != tc.want {
				t.Errorf("CanShow = %v, want %v", got, tc.want)
			}
		})
	}
}
