package ui

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func (k key) String() string {
	return [...]string{"other", "up", "down", "enter", "space", "esc", "ctrl-c"}[k]
}

// keysOf decodes every key press of input.
func keysOf(t *testing.T, input string) []key {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(input))
	var got []key
	for range len(input) + 1 {
		k, err := readKey(r)
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatalf("readKey: %v", err)
		}
		got = append(got, k)
	}
	t.Fatalf("readKey did not reach the end of %q", input)
	return nil
}

// A terminal sends an arrow as an escape sequence in one write, so what is buffered after the Esc tells a bare Esc from a sequence.
func TestReadKeyDecodesTheKeysOfASelect(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []key
	}{
		{"up, CSI", "\x1b[A", []key{keyUp}},
		{"down, CSI", "\x1b[B", []key{keyDown}},
		{"up, SS3", "\x1bOA", []key{keyUp}},
		{"down, SS3", "\x1bOB", []key{keyDown}},
		{"up with a modifier", "\x1b[1;5A", []key{keyUp}},
		{"down with a modifier", "\x1b[1;2B", []key{keyDown}},
		{"Enter, CR", "\r", []key{keyEnter}},
		{"Enter, LF", "\n", []key{keyEnter}},
		{"Enter, CRLF is one press", "\r\n", []key{keyEnter}},
		{"two Enters", "\r\r", []key{keyEnter, keyEnter}},
		{"space", " ", []key{keySpace}},
		{"Ctrl-C as a character", "\x03", []key{keyCtrlC}},
		{"a bare Esc", "\x1b", []key{keyEsc}},
		{"a bare Esc after a key", "\x1b[B\x1b", []key{keyDown, keyEsc}},
		{"right and left are ignored", "\x1b[C\x1b[D", []key{keyOther, keyOther}},
		{"Delete is one ignored press", "\x1b[3~", []key{keyOther}},
		{"a function key (SS3) is one ignored press", "\x1bOP", []key{keyOther}},
		{"a mouse report is one ignored press", "\x1b[<0;10;5M", []key{keyOther}},
		{"Alt and a letter is one ignored press", "\x1bx", []key{keyOther}},
		{"a letter is ignored", "q", []key{keyOther}},
		{"a letter that is two bytes is ignored byte by byte", "é", []key{keyOther, keyOther}},
		{"a sequence, then Enter", "\x1b[B\r", []key{keyDown, keyEnter}},
		{"nothing", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := keysOf(t, tt.input); !slices.Equal(got, tt.want) {
				t.Fatalf("%q decodes to %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
