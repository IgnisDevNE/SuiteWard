// Package app composes the SuiteWard service: it loads the settings and runs
// the commands of the runtime contract (docs/contracts/runtime.md).
package app

import (
	"context"
	"errors"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Version is the build version that /status reports. The release build sets it:
// -ldflags "-X github.com/IgnisDevNE/SuiteWard/internal/app.Version=1.2.3".
var Version = "dev"

// Env is the process environment that the settings are read from.
type Env struct {
	Lookup   func(string) (string, bool)
	Environ  []string
	ReadFile func(string) ([]byte, error)
}

// OSEnv is the real process environment and filesystem.
func OSEnv() Env {
	lookup, environ, readFile := config.OS()
	return Env{Lookup: lookup, Environ: environ, ReadFile: readFile}
}

// Run executes one command (args without the program name) and returns the
// process exit code of the runtime contract.
func Run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	return -1
}

var errUnimplemented = errors.New("not implemented")
