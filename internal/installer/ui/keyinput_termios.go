//go:build linux || darwin

package ui

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// enableKeys puts the terminal input behind f in per-key mode: no line editing and no echo, and a read returns as soon as one byte is
// there. Signal generation, the translation of CR to NL and the output processing are left alone, so Ctrl-C is still SIGINT, which
// cancels the run, and never a character, and the screen's output is not touched. term.MakeRaw would clear all of them. The returned
// function puts the previous mode back. What the user typed before, while nothing read it, is dropped: the terminal queued it, and it would
// answer the question that opens now.
func enableKeys(f *os.File) (func() error, error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, fmt.Errorf("read the terminal mode: %w", err)
	}
	keys := *old
	keys.Lflag &^= unix.ICANON | unix.ECHO
	keys.Cc[unix.VMIN], keys.Cc[unix.VTIME] = 1, 0 // in canonical mode these slots hold other things
	if err := unix.IoctlSetTermios(fd, ioctlSetTermiosFlush, &keys); err != nil {
		return nil, fmt.Errorf("enable per-key input: %w", err)
	}
	return func() error {
		if err := unix.IoctlSetTermios(fd, ioctlSetTermios, old); err != nil {
			return fmt.Errorf("restore the terminal mode: %w", err)
		}
		return nil
	}, nil
}

// flushInput drops what the user typed at the terminal behind f and nothing has read, and leaves its mode as it is.
func flushInput(*os.File) error { return nil }
