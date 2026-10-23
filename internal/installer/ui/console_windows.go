//go:build windows

package ui

import (
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
