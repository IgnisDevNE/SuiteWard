//go:build linux || darwin

package ui

import (
	"context"
	"os"
)

// inputReadiness is the wait for a key of the terminal behind f that Options.InputReady is, or nil where it cannot be done.
func inputReadiness(*os.File) func(context.Context) error { return nil }
