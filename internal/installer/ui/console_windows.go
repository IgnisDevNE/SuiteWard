//go:build windows

package ui

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on virtual-terminal processing for the console behind f, so that escape sequences draw instead of printing. The
// returned function puts the previous console mode back.
func enableVT(f *os.File) (func() error, error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, fmt.Errorf("read the console mode: %w", err)
	}
	if err := windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, fmt.Errorf("enable virtual-terminal processing: %w", err)
	}
	return func() error {
		if err := windows.SetConsoleMode(h, mode); err != nil {
			return fmt.Errorf("restore the console mode: %w", err)
		}
		return nil
	}, nil
}

// enableKeys puts the console input behind f in per-key mode: no line editing and no echo, so a key press is read when it is made, and
// virtual-terminal input, so that an arrow arrives as an escape sequence. Processed input stays on (it is switched on if it was off),
// so Ctrl-C is still the signal that cancels the run and never a character. The returned function puts the previous mode back. What the user
// typed before, while nothing read it, is dropped: the console queued it, and it would answer the question that opens now.
func enableKeys(f *os.File) (func() error, error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, fmt.Errorf("read the console input mode: %w", err)
	}
	keys := mode&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(h, keys); err != nil {
		return nil, fmt.Errorf("enable per-key input: %w", err)
	}
	if err := windows.FlushConsoleInputBuffer(h); err != nil {
		return nil, errors.Join(fmt.Errorf("drop the keys typed before: %w", err), windows.SetConsoleMode(h, mode))
	}
	return func() error {
		if err := windows.SetConsoleMode(h, mode); err != nil {
			return fmt.Errorf("restore the console input mode: %w", err)
		}
		return nil
	}, nil
}

// flushInput drops what the user typed in the console behind f and nothing has read, and leaves its mode as it is. A file is no console: that
// is an error.
func flushInput(f *os.File) error {
	if err := windows.FlushConsoleInputBuffer(windows.Handle(f.Fd())); err != nil {
		return fmt.Errorf("drop the keys typed before: %w", err)
	}
	return nil
}
