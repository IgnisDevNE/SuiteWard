//go:build !windows && !linux && !darwin

package ui

import (
	"context"
	"os"
)

// inputReadiness is nil here: the terminal input cannot be waited on without reading it.
func inputReadiness(*os.File) func(context.Context) error { return nil }
