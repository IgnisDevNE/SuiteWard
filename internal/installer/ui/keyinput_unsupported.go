//go:build !windows && !linux && !darwin

package ui

import (
	"errors"
	"os"
)

// enableKeys cannot put the terminal in per-key mode here: Select asks by number.
func enableKeys(*os.File) (func() error, error) {
	return nil, errors.New("per-key input is not supported on this platform")
}

// flushInput cannot drop the keys that were typed ahead here: the reader of the screen still drops what it holds unused.
func flushInput(*os.File) error { return nil }
