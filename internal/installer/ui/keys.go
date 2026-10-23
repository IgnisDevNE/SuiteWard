package ui

import (
	"bufio"
	"io"
)

// key is one key press that Select understands.
type key int

const (
	keyOther key = iota // anything else: ignored
	keyUp
	keyDown
	keyEnter
	keySpace
	keyEsc
	keyCtrlC
)

// readKey reads one key press from r. It does nothing yet.
func readKey(*bufio.Reader) (key, error) { return keyOther, io.EOF }
