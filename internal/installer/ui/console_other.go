//go:build !windows

package ui

import "os"

// enableVT does nothing here: a terminal that is a TTY takes escape sequences as it is.
func enableVT(*os.File) (func() error, error) {
	return func() error { return nil }, nil
}

// enableKeys puts the terminal input behind f in per-key mode. It does nothing yet.
func enableKeys(*os.File) (func() error, error) { return func() error { return nil }, nil }
