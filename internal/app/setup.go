package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

var errNotImplemented = errors.New("not implemented")

// setupFlags are the flags of `setup`, before they are matched with the configured repositories.
type setupFlags struct {
	owner  string
	checks []string // repeated --test-check owner/name=check
}

// setupOptions is a usage-checked setup: one test check per configured repository.
type setupOptions struct {
	owner      string
	testChecks map[string]string // by configured repository
}

// parseSetup reads the flags of `setup`. An error has been reported on stderr with the usage.
func parseSetup(args []string, stderr io.Writer) (setupFlags, error) {
	return setupFlags{}, errNotImplemented
}

// options matches the test checks with the configured repositories.
func (f setupFlags) options(repos []string) (setupOptions, error) {
	return setupOptions{}, errNotImplemented
}

// setupReaders are the two GitHub readers of setup: the synchronization token's and the protection token's.
type setupReaders struct{ read, protect *github.Reader }

func composeSetupReaders(g config.GitHub) (setupReaders, error) {
	return setupReaders{}, errNotImplemented
}

// setup is one run of the setup command over its collaborators.
type setup struct {
	cfg     config.Config
	options setupOptions
	store   owner.Store
	readers setupReaders
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	logger  *slog.Logger
	now     func() time.Time
	random  io.Reader
}

// run enrolls the owner and verifies protection once per repository, and
// returns the exit code.
func (s *setup) run(ctx context.Context) int {
	reportf(s.stderr, "%v\n", errNotImplemented)
	return 1
}

// protectionFacts maps what the adapter read into the evaluator's facts.
func protectionFacts(p github.Protection, private bool) (protection.Facts, error) {
	return protection.Facts{}, errNotImplemented
}

// runSetup migrates the database, composes the store and the readers of cfg and runs setup.
func runSetup(ctx context.Context, cfg config.Config, o setupOptions, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger) int {
	reportf(stderr, "%v\n", errNotImplemented)
	return 1
}
