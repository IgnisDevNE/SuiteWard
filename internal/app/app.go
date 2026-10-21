// Package app composes the SuiteWard service: it loads the settings and runs
// the commands of the runtime contract (docs/contracts/runtime.md).
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Version is the build version that /status reports. The release build sets it:
// -ldflags "-X github.com/IgnisDevNE/SuiteWard/internal/app.Version=1.2.3".
var Version = "dev"

// Source is the URL of the source revision the binary was built from, which /status reports (AGPL section 13).
// The release build sets it like Version: -ldflags "-X github.com/IgnisDevNE/SuiteWard/internal/app.Source=https://...".
var Source string

const usage = `usage: suiteward serve
       suiteward migrate
       suiteward probe [--delay D] [--no-wait] [--wait ID] [--timeout D]
       suiteward setup --owner LOGIN --test-check OWNER/NAME=CHECK [--test-check ...]
`

// Env is the process environment that the settings are read from, and the
// input of the interactive commands.
type Env struct {
	Lookup   func(string) (string, bool)
	Environ  []string
	ReadFile func(string) ([]byte, error)
	Stdin    io.Reader
}

// OSEnv is the real process environment, filesystem and standard input.
func OSEnv() Env {
	lookup, environ, readFile := config.OS()
	return Env{Lookup: lookup, Environ: environ, ReadFile: readFile, Stdin: os.Stdin}
}

// Run executes one command (args without the program name) and returns the
// process exit code of the runtime contract. serve and migrate log JSON to stdout; probe
// prints JSON lines to stdout; setup prints only the TOTP enrollment to stdout
// and logs JSON to stderr; usage and problems go to stderr.
func Run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	return runWith(ctx, args, env, stdout, stderr, migrateWait{attempts: 30, interval: time.Second, timeout: 5 * time.Second})
}

// runWith is Run with the wait of migrate, which a test bounds.
func runWith(ctx context.Context, args []string, env Env, stdout, stderr io.Writer, wait migrateWait) int {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	var (
		probe probeOptions
		flags setupFlags
	)
	switch command {
	case "serve", "migrate":
		if len(args) != 1 {
			return usageError(stderr)
		}
	case "probe":
		var err error
		if probe, err = parseProbe(args[1:], stderr); err != nil {
			return 2
		}
	case "setup":
		var err error
		if flags, err = parseSetup(args[1:], stderr); err != nil {
			return 2
		}
	default:
		return usageError(stderr)
	}

	cfg, warnings, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	logs := stdout
	if command == "setup" { // stdout carries the TOTP secret and nothing else
		logs = stderr
	}
	logger := newLogger(logs, cfg.LogLevel)
	if command == "serve" || command == "migrate" {
		for _, name := range warnings {
			logger.Warn("unrecognized setting", "name", name)
		}
	}
	if err != nil {
		reportf(stderr, "invalid settings:\n%v\n", err)
		return 1
	}
	if command != "migrate" && cfg.MigrationDatabaseURL != "" { // the owner credential never reaches the service
		reportf(stderr, "the migration credential is for `suiteward migrate` only; remove it from this service's environment\n")
		return 1
	}
	var setupOpts setupOptions
	if command == "setup" {
		if cfg.GitHub == nil {
			reportf(stderr, "setup needs the GitHub settings (SUITEWARD_GITHUB_*)\n")
			return usageError(stderr)
		}
		if setupOpts, err = flags.options(cfg.GitHub.Repositories); err != nil {
			reportf(stderr, "%v\n", err)
			return usageError(stderr)
		}
	}
	// The cause is dropped: pgx redacts only up to the first @, so for an unencoded @ in the password it echoes the rest.
	service, err := pgxpool.ParseConfig(cfg.DatabaseURL.Reveal())
	if err != nil {
		reportf(stderr, "invalid settings:\nSUITEWARD_DATABASE_URL is not a valid PostgreSQL connection string\n")
		return 1
	}
	if command == "migrate" {
		if cfg.MigrationDatabaseURL == "" {
			reportf(stderr, "migrate needs the owner connection (SUITEWARD_MIGRATION_DATABASE_URL)\n")
			return usageError(stderr)
		}
		if _, err := pgxpool.ParseConfig(cfg.MigrationDatabaseURL.Reveal()); err != nil {
			reportf(stderr, "invalid settings:\nSUITEWARD_MIGRATION_DATABASE_URL is not a valid PostgreSQL connection string\n")
			return 1
		}
	}
	switch command {
	case "serve":
		return serve(ctx, cfg, logger, nil)
	case "migrate":
		// The role the service connects as is the user of its own URL: nothing else can say which role will run.
		return runMigrate(ctx, cfg, service.ConnConfig.User, wait, stderr, logger)
	case "setup":
		return runSetup(ctx, cfg, setupOpts, env.Stdin, stdout, stderr, logger)
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
