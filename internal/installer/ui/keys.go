package ui

import (
	"bufio"
	"errors"
	"io"
)

// key is one key press that Select and MultiSelect understand.
type key int

const (
	keyOther key = iota // anything else: ignored
	keyUp
	keyDown
	keyEnter
	keySpace
	keyEsc
	keyCtrlC // only where Ctrl-C reaches the program as a character; a console that keeps its signals raises one instead
)

// readKey reads one key press from r, which may hold more input than the press. A terminal sends an arrow as one escape sequence, in one
// write, so what is buffered after an Esc tells a bare Esc from the start of a sequence. Both CSI (ESC [ ... A) and SS3 (ESC O A) arrows
// are read, with or without a modifier. An Enter that arrives as CR LF is one press, so the LF is not left for the next question.
// shortcut: a sequence that a slow link splits across two reads is a bare Esc and ignored characters, so the arrow is lost; upgrade with
// a short wait for the byte after Esc if a real link does that.
func readKey(r *bufio.Reader) (key, error) {
	b, err := r.ReadByte()
	if err != nil {
		return keyOther, err
	}
	switch b {
	case '\r':
		if r.Buffered() > 0 {
			if next, _ := r.Peek(1); next[0] == '\n' { // buffered, so it cannot fail
				_, _ = r.Discard(1)
			}
		}
		return keyEnter, nil
	case '\n':
		return keyEnter, nil
	case ' ':
		return keySpace, nil
	case 0x03:
		return keyCtrlC, nil
	case 0x1b:
		return readEscape(r), nil
	}
	return keyOther, nil
}

// readEscape reads what follows an Esc that has been read. Every byte it takes is buffered, so none of the reads can fail.
func readEscape(r *bufio.Reader) key {
	if r.Buffered() == 0 {
		return keyEsc
	}
	intro, _ := r.ReadByte()
	switch intro {
	case 'O': // SS3: one more byte
		if r.Buffered() == 0 {
			return keyOther
		}
		final, _ := r.ReadByte()
		return arrow(final)
	case '[': // CSI: parameters and intermediates, then a final byte 0x40 to 0x7e
		for r.Buffered() > 0 {
			if c, _ := r.ReadByte(); c >= 0x40 && c <= 0x7e {
				return arrow(c)
			}
		}
	}
	return keyOther // Alt and a key, or a sequence that is cut short: the key is taken
}

func arrow(final byte) key {
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	}
	return keyOther
}

// readHiddenLine reads the text of a line from r in key mode, where the terminal does no line editing: characters are taken as they
// come, backspace (DEL or BS) drops the last character, Ctrl-U the whole line, an escape sequence (an arrow, Delete) and the other
// control characters are not text, and CR, LF or CR LF ends the line. The last line may lack its newline; the end of the input before
// any character is io.EOF. A Ctrl-C that arrives as a character is errCtrlC.
func readHiddenLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		b, err := r.ReadByte()
		switch {
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		case err != nil:
			return nil, err
		case b == '\r':
			if r.Buffered() > 0 {
				if next, _ := r.Peek(1); next[0] == '\n' { // buffered, so it cannot fail
					_, _ = r.Discard(1)
				}
			}
			return line, nil
		case b == '\n':
			return line, nil
		case b == 0x03:
			return nil, errCtrlC
		case b == 0x7f || b == 0x08:
			line = dropRune(line)
		case b == 0x15:
			line = line[:0]
		case b == 0x1b:
			readEscape(r)
		case b >= 0x20:
			line = append(line, b)
		}
	}
}

// dropRune removes the last character of b: its continuation bytes and the byte that starts it.
func dropRune(b []byte) []byte {
	for len(b) > 0 {
		last := b[len(b)-1]
		b = b[:len(b)-1]
		if last&0xc0 != 0x80 {
			break
		}
	}
	return b
}
