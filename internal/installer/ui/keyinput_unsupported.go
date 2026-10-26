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

// flushInput cannot drop the keys that were typed ahead here: it fails, so that no flush is offered to the screen (see inputFlusher) instead of
// a no-op that looks like one. The reader of the screen still drops what it holds unused.
func flushInput(*os.File) error {
	return errors.New("dropping the keys typed ahead is not supported on this platform")
}
