package commandtext_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A rune that is graphic to Unicode yet draws nothing (a joiner filler, a variation selector, a braille blank) hides text from the owner
// as a bidi control does. One predicate denies them in every place the comments show repository text.
var invisibleRunes = []rune{0x034f, 0x115f, 0x1160, 0x17b4, 0x17b5, 0x180b, 0x180d, 0x180f, 0x2800, 0x3164, 0xfe00, 0xfe0f, 0xffa0, 0xe0100, 0xe01ef}

func TestDisplayEscapesAnInvisibleRune(t *testing.T) {
	for _, r := range invisibleRunes {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			r2 := revision(commandtext.Isolated)
			r2.Added = []string{"tests/a" + string(r) + "b_test.go"}
			body := revisionText(t, r2)
			if strings.ContainsRune(body, r) {
				t.Errorf("the comment shows the rune as it is:\n%s", body)
			}
			escape := fmt.Sprintf(`\u%04x`, r)
			if r > 0xffff {
				escape = fmt.Sprintf(`\U%08x`, r)
			}
			if !strings.Contains(body, "tests/a"+escape+"b_test.go") {
				t.Errorf("the comment does not show %s:\n%s", escape, body)
			}
		})
	}
}

func TestADeclarationWithAnInvisibleRuneCannotBeShown(t *testing.T) {
	for _, r := range invisibleRunes {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			if commandtext.CanShow([]byte("version: 1\n# a" + string(r) + "b\n")) {
				t.Error("CanShow accepts it")
			}
		})
	}
}

// A byte sequence that is not UTF-8 stays invalid when the layout between its bytes is removed: the check runs on the raw bytes.
func TestADeclarationWhoseBytesAreOnlyValidOnceLayoutIsRemovedCannotBeShown(t *testing.T) {
	for name, declaration := range map[string]string{
		"a line feed inside a sequence": "\xc3\n\x85",
		"a tab inside a sequence":       "\xe2\t\xa0\x80",
		"a tab inside a two-byte one":   "\xc9\t\xb1",
		"a CRLF inside a sequence":      "\xc3\r\n\x85",
	} {
		t.Run(name, func(t *testing.T) {
			if commandtext.CanShow([]byte(declaration)) {
				t.Errorf("CanShow accepts %q", declaration)
			}
		})
	}
}

func TestASuggestedPatternWithAnInvisibleRuneIsNotSuggested(t *testing.T) {
	for _, r := range invisibleRunes {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			body := suggestionText(t, missingWith([]string{"tests/**", "docs/a" + string(r) + "b/**"}, nil, nil, false))
			if strings.ContainsRune(body, r) {
				t.Errorf("the comment suggests the pattern:\n%s", body)
			}
		})
	}
}
