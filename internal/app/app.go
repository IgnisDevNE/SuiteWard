// Package app composes the SuiteWard service: it loads the settings and runs
// the commands of the runtime contract (docs/contracts/runtime.md).
package app

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Version is the build version that /status reports. The release build sets it:
// -ldflags "-X github.com/IgnisDevNE/SuiteWard/internal/app.Version=1.2.3".
var Version = "dev"

const usage = `usage: suiteward serve
       suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]
`

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
// process exit code of the runtime contract. serve logs JSON to stdout; probe
// prints JSON lines to stdout; usage and problems go to stderr.
func Run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	var probe probeOptions
	switch command {
	case "serve":
		if len(args) != 1 {
			return usageError(stderr)
		}
	case "probe":
		var err error
		if probe, err = parseProbe(args[1:], stderr); err != nil {
			return 2
		}
	default:
		return usageError(stderr)
	}

	cfg, warnings, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	logger := newLogger(stdout, cfg.LogLevel)
	if command == "serve" {
		for _, name := range warnings {
			logger.Warn("unrecognized setting", "name", name)
		}
	}
	if err != nil {
		reportf(stderr, "invalid settings:\n%v\n", err)
		return 1
	}
	// The cause is dropped: pgx redacts only up to the first @, so for an unencoded @ in the password it echoes the rest.
	if _, err := pgxpool.ParseConfig(cfg.DatabaseURL.Reveal()); err != nil {
		reportf(stderr, "invalid settings:\nSUITEWARD_DATABASE_URL is not a valid PostgreSQL connection string\n")
		return 1
	}
	if command == "serve" {
		return serve(ctx, cfg, logger, nil)
	}
	return runProbe(ctx, cfg, probe, stdout, stderr)
}

func usageError(stderr io.Writer) int {
	// A failed write to stderr cannot be reported anywhere else.
	_, _ = io.WriteString(stderr, usage)
	return 2
}

// reportf prints a problem to stderr.
func reportf(stderr io.Writer, format string, args ...any) {
	// A failed write to stderr cannot be reported anywhere else.
	_, _ = fmt.Fprintf(stderr, "suiteward: "+format, args...)
}
