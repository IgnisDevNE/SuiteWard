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
