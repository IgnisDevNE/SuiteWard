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
	"github.com/IgnisDevNE/SuiteWard/internal/installer"
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
       suiteward check [--json]
       suiteward install [flags]
       suiteward install --upgrade [--yes] [--backup-dir DIR] [--backup-recipient age1pq1...] [--image sha256:<digest>] [flags]
       suiteward install host [flags]
       suiteward uninstall [--purge] [--yes] [--instance ID] [flags]
       suiteward enroll [--owner LOGIN] [--test-check OWNER/NAME=CHECK ...] [--settings PATH] [--tier isolated] [--plain]
       suiteward backup init [--kit PATH] [--yes] [--replace] [--settings PATH] [--tier isolated] [--plain]
       suiteward backup [--to DIR] [--settings PATH] [--tier isolated] [--plain]
       suiteward restore <backup.tar.age> [--kit PATH] [--yes --instance ID] [--settings PATH] [--tier isolated] [--plain]
       suiteward restore --plaintext-dir DIR [--yes --instance ID] [--settings PATH] [--tier isolated] [--plain]
       suiteward doctor [--json] [--settings PATH] [--plain]
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
	// The commands of the machine that installs run before the settings are read: it has no SUITEWARD_* environment.
	if len(args) > 0 && (args[0] == "install" || args[0] == "uninstall" || args[0] == "enroll" || args[0] == "backup" || args[0] == "restore" || args[0] == "doctor") {
		return installer.Main(ctx, args, installerEnv(stdout), stderr)
	}
	return runWith(ctx, args, env, stdout, stderr, migrateWait{attempts: 30, interval: time.Second, timeout: 5 * time.Second})
}

// commandFlags are the parsed flags of the commands that have any.
type commandFlags struct {
	probe  probeOptions
	setup  setupFlags
	asJSON bool // check --json
}

// parseCommand reads the arguments of command, which are the ones after its name. A nonzero exit code means the
// usage error has been reported on stderr.
func parseCommand(command string, args []string, stderr io.Writer) (flags commandFlags, exit int) {
	var err error
	switch command {
	case "serve", "migrate":
		if len(args) != 0 {
			return flags, usageError(stderr)
		}
	case "probe":
		flags.probe, err = parseProbe(args, stderr)
	case "setup":
		flags.setup, err = parseSetup(args, stderr)
	case "check":
		flags.asJSON, err = parseSelfCheck(args, stderr)
	default:
		return flags, usageError(stderr)
	}
	if err != nil {
		return flags, 2
	}
	return flags, 0
}

// runWith is Run with the wait of migrate, which a test bounds.
func runWith(ctx context.Context, args []string, env Env, stdout, stderr io.Writer, wait migrateWait) int {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	flags, exit := parseCommand(command, args[min(1, len(args)):], stderr)
	if exit != 0 {
		return exit
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
		if setupOpts, err = flags.setup.options(cfg.GitHub.Repositories); err != nil {
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
	case "check":
		return runSelfCheck(ctx, cfg, flags.asJSON, stdout, stderr)
	}
	return runProbe(ctx, cfg, flags.probe, stdout, stderr)
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

// installerEnv is the machine and the console of the installer's commands, which show the build version in the title bar of the full screen.
func installerEnv(stdout io.Writer) installer.Env {
	env := installer.OSEnv(stdout)
	env.Version = Version
	return env
}
