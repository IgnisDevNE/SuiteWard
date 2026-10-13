package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

const (
	// totpIssuer names SuiteWard in the owner's authenticator app.
	totpIssuer = "SuiteWard"
	// totpAttempts is how many codes setup reads before it gives up.
	totpAttempts = 3
	// totpTolerance is how many steps either side of now a code may be from (a skewed phone clock).
	totpTolerance = 1
)

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
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, usage) } // a failed write to stderr cannot be reported anywhere else
	var f setupFlags
	flags.StringVar(&f.owner, "owner", "", "the GitHub login of the instance owner")
	flags.Func("test-check", "OWNER/NAME=CHECK, the required test check of a configured repository (once per repository)", func(v string) error {
		f.checks = append(f.checks, v)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return f, err
	}
	var err error
	switch {
	case strings.TrimSpace(f.owner) == "":
		err = errors.New("--owner is required")
	case flags.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if err != nil {
		reportf(stderr, "%v\n", err)
		_, _ = io.WriteString(stderr, usage) // a failed write to stderr cannot be reported anywhere else
	}
	return f, err
}

// options matches the test checks with the configured repositories: exactly
// one per repository. A check is everything after the first =, so it may
// contain slashes, spaces and further = signs.
func (f setupFlags) options(repos []string) (setupOptions, error) {
	checks := make(map[string]string, len(repos))
	for _, raw := range f.checks {
		repo, check, found := strings.Cut(raw, "=")
		switch {
		case !found:
			return setupOptions{}, fmt.Errorf("--test-check %q must be OWNER/NAME=CHECK", raw)
		case !slices.Contains(repos, repo):
			return setupOptions{}, fmt.Errorf("--test-check names %s, which is not a configured repository", repo)
		case strings.TrimSpace(check) == "":
			return setupOptions{}, fmt.Errorf("--test-check for %s has an empty check name", repo)
		case len(check) > owner.MaxTestCheckBytes:
			return setupOptions{}, fmt.Errorf("--test-check for %s is longer than %d bytes", repo, owner.MaxTestCheckBytes)
		}
		if _, listed := checks[repo]; listed {
			return setupOptions{}, fmt.Errorf("--test-check is given twice for %s", repo)
		}
		checks[repo] = check
	}
	for _, repo := range repos {
		if _, listed := checks[repo]; !listed {
			return setupOptions{}, fmt.Errorf("--test-check is missing for %s", repo)
		}
	}
	return setupOptions{owner: strings.TrimSpace(f.owner), testChecks: checks}, nil
}

// setupReaders are the two GitHub readers of setup: the synchronization
// token's, for the lookups, and the protection token's. They share one budget.
type setupReaders struct{ read, protect *github.Reader }

func composeSetupReaders(g config.GitHub) (setupReaders, error) {
	access, err := newGitHubAccess(g)
	if err != nil {
		return setupReaders{}, err
	}
	read, err := access.reader(syncPermissions)
	if err != nil {
		return setupReaders{}, err
	}
	protect, err := access.reader(protectionPermissions)
	if err != nil {
		return setupReaders{}, err
	}
	return setupReaders{read: read, protect: protect}, nil
}

// setup is one run of the setup command over its collaborators. stdout carries
// the enrollment and nothing else; everything else, logs included, is on stderr.
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

// resolved is what the lookups found; nothing is written before all of it is.
type resolved struct {
	user  github.User
	repos []github.Repository // in configuration order
}

// run enrolls the owner and verifies protection once per repository, and
// returns the exit code: 0 when the enrollment succeeded, whatever the
// protection results, and 1 for anything else.
func (s *setup) run(ctx context.Context) int {
	found, ok := s.lookups(ctx)
	if !ok {
		return 1
	}
	secret, step, ok := s.enrollmentCode(found.user.Login)
	if !ok {
		return 1
	}
	if !s.enroll(ctx, found, secret, step) {
		return 1
	}
	s.verifyProtection(ctx, found)
	return 0
}

// lookups resolves the owner, which must be a User, and every configured
// repository. A failure is reported with fixed text; its cause is logged as
// the repository, the classification and the HTTP status, never text GitHub sent.
func (s *setup) lookups(ctx context.Context) (resolved, bool) {
	user, err := s.readers.read.User(ctx, s.options.owner)
	if err != nil {
		s.logRead(ctx, "github lookup failed", "", err)
		reportf(s.stderr, "could not look up the owner %q on GitHub; see the log\n", s.options.owner)
		return resolved{}, false
	}
	if user.Type != "User" {
		reportf(s.stderr, "%q is not a GitHub user account: the owner must be a person's own account, not an organization or a bot\n", s.options.owner)
		return resolved{}, false
	}
	repos := make([]github.Repository, len(s.cfg.GitHub.Repositories))
	for i, name := range s.cfg.GitHub.Repositories {
		if repos[i], err = s.readers.read.Repository(ctx, name); err != nil {
			s.logRead(ctx, "github lookup failed", name, err)
			reportf(s.stderr, "could not look up the repository %s on GitHub: check that the configured installation covers it; see the log\n", name)
			return resolved{}, false
		}
	}
	return resolved{user: user, repos: repos}, true
}

// logRead logs a failed GitHub read: the repository, the classification and the HTTP status.
func (s *setup) logRead(ctx context.Context, msg, repo string, err error) {
	failure := readFailure(err)
	s.logger.WarnContext(ctx, msg, "repo", repo, "kind", string(failure.Kind), "status", failure.Status)
}

// enrollmentCode generates the TOTP secret, prints it once and reads a code
// that verifies against it, at most totpAttempts times. It returns the secret
// and the step that matched.
func (s *setup) enrollmentCode(login string) (secret []byte, step int64, ok bool) {
	secret, err := totp.NewSecret(s.random)
	if err != nil {
		reportf(s.stderr, "could not generate the TOTP secret\n")
		s.logger.Error("generate the TOTP secret", "error", err)
		return nil, 0, false
	}
	uri, err := totp.URI(secret, totpIssuer, login)
	if err != nil {
		reportf(s.stderr, "could not build the TOTP enrollment\n")
		s.logger.Error("build the TOTP URI", "error", err)
		return nil, 0, false
	}
	reportf(s.stderr, "warning: a recorded terminal session (for example Session Manager logging) keeps the TOTP secret printed next. If this session is recorded, press Ctrl-C now, disable the logging, and run setup again.\n")
	if _, err := fmt.Fprintf(s.stdout, "%s\n%s\n", uri, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)); err != nil {
		reportf(s.stderr, "could not write the TOTP enrollment to the output\n")
		return nil, 0, false
	}
	reportf(s.stderr, "add it to your authenticator app (the otpauth:// URI above, or the base32 secret), then confirm with a code.\n")

	lines := bufio.NewScanner(s.stdin)
	for attempt := 1; attempt <= totpAttempts; attempt++ {
		_, _ = fmt.Fprintf(s.stderr, "Enter the 6-digit code (attempt %d of %d): ", attempt, totpAttempts) // a failed write to stderr cannot be reported anywhere else
		if !lines.Scan() {
			break
		}
		step, err := totp.Verify(secret, strings.TrimSpace(lines.Text()), s.now(), totpTolerance)
		switch {
		case err == nil:
			return secret, step, true
		case errors.Is(err, totp.ErrInvalidCode):
			_, _ = fmt.Fprintln(s.stderr, "That code did not match.")
		default:
			reportf(s.stderr, "could not verify the code\n")
			s.logger.Error("verify the TOTP code", "error", err)
			return nil, 0, false
		}
	}
	reportf(s.stderr, "\nno valid code; nothing was written. Remove the entry from the authenticator app.\n")
	return nil, 0, false
}

// enroll records the owner, the secret, one project per repository and the
// verified step in one call of the store.
func (s *setup) enroll(ctx context.Context, found resolved, secret []byte, step int64) bool {
	projects := make([]owner.ProjectPolicy, len(found.repos))
	for i, repo := range found.repos {
		name := s.cfg.GitHub.Repositories[i]
		projects[i] = owner.ProjectPolicy{RepositoryID: repo.ID, Repo: name, TestCheck: s.options.testChecks[name]}
	}
	err := s.store.Enroll(ctx, owner.Enrollment{
		Owner: owner.Owner{AccountID: found.user.ID, Login: found.user.Login}, Secret: secret, Projects: projects,
		Step: step, Tier: string(s.cfg.Tier), EnrolledAt: s.now(),
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, owner.ErrStepUsed):
		reportf(s.stderr, "that code was already used; nothing was written. Wait for the next code, remove the entry from the authenticator app and run setup again\n")
	case errors.Is(err, owner.ErrInvalidState):
		reportf(s.stderr, "the enrollment was refused and nothing was written: %v\n", err) // the owner package's own words; no secret
	default:
		s.logger.ErrorContext(ctx, "record the enrollment", "error", err)
		reportf(s.stderr, "could not record the enrollment; nothing was written. See the log\n")
	}
	return false
}

// verifyProtection evaluates and records the protection of every repository
// and prints each result with its instructions. A result that cannot be stored
// is reported and logged, not an exit code: the enrollment has succeeded.
func (s *setup) verifyProtection(ctx context.Context, found resolved) {
	for i, repo := range found.repos {
		name := s.cfg.GitHub.Repositories[i]
		result := s.evaluate(ctx, name, repo, s.options.testChecks[name])
		_, _ = fmt.Fprintf(s.stderr, "%s: %s\n", name, result.Kind) // a failed write to stderr cannot be reported anywhere else
		for _, instruction := range result.Instructions {
			_, _ = fmt.Fprintf(s.stderr, "  - %s\n", instruction)
		}
		err := s.store.RecordProtection(ctx, owner.ProtectionResult{
			RepositoryID: repo.ID, Result: string(result.Kind), Findings: codeStrings(result.Findings), Caveats: codeStrings(result.Caveats), CheckedAt: s.now(),
		})
		if err != nil {
			s.logger.ErrorContext(ctx, "record the protection result", "repo", name, "error", err)
			reportf(s.stderr, "the owner is enrolled, but the protection result of %s could not be stored; see the log\n", name)
		}
	}
}

// evaluate reads the protection of the default branch with the protection
// token and evaluates it. A read that fails, for whatever reason, is the
// result unknown: facts are mapped only from a successful read.
func (s *setup) evaluate(ctx context.Context, name string, repo github.Repository, testCheck string) protection.Result {
	read, err := s.readers.protect.Protection(ctx, name, repo.DefaultBranch)
	if err != nil {
		s.logRead(ctx, "github protection read failed", name, err)
		return protection.Unknown()
	}
	facts, err := protectionFacts(read, repo.Private)
	if err != nil {
		s.logger.WarnContext(ctx, "github protection answer is not usable", "repo", name)
		return protection.Unknown()
	}
	return protection.Evaluate(facts, s.cfg.GitHub.AppID, testCheck)
}

func codeStrings(codes []protection.Code) []string {
	var out []string
	for _, c := range codes {
		out = append(out, string(c))
	}
	return out
}

// protectionFacts maps what the adapter read, field by field, into the
// evaluator's facts; private comes from the repository read.
func protectionFacts(p github.Protection, private bool) (protection.Facts, error) {
	state, err := classicState(p.Classic.State)
	if err != nil {
		return protection.Facts{}, err
	}
	facts := protection.Facts{
		Private: private, RulesForbidden: p.RulesForbidden, FromRuleset: p.FromRuleset,
		Classic: protection.Classic{State: state, Checks: protectionChecks(p.Classic.Checks), Strict: p.Classic.Strict, EnforceAdmins: p.Classic.EnforceAdmins},
	}
	for _, rule := range p.Rules {
		facts.Rules = append(facts.Rules, protection.Rule{RulesetID: rule.RulesetID, Checks: protectionChecks(rule.Checks), Strict: rule.Strict})
	}
	return facts, nil
}

func classicState(state github.ClassicState) (protection.ClassicState, error) {
	switch state {
	case github.ClassicAbsent:
		return protection.ClassicAbsent, nil
	case github.ClassicUnreadable:
		return protection.ClassicUnreadable, nil
	case github.ClassicPresent:
		return protection.ClassicPresent, nil
	}
	return 0, fmt.Errorf("unknown classic protection state %d", state)
}

func protectionChecks(checks []github.Check) []protection.Check {
	var out []protection.Check
	for _, c := range checks {
		out = append(out, protection.Check{Context: c.Context, SourceID: c.SourceID})
	}
	return out
}

// runSetup migrates the database, composes the store and the GitHub readers
// of cfg (whose GitHub settings Run has checked) and runs setup.
func runSetup(ctx context.Context, cfg config.Config, o setupOptions, stdin io.Reader, stdout, stderr io.Writer, logger *slog.Logger) int {
	if err := migrations.Up(ctx, cfg.DatabaseURL.Reveal()); err != nil {
		reportf(stderr, "migrate the database: %v\n", err)
		return 1
	}
	pool, store, err := connect(ctx, cfg)
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	defer pool.Close()
	readers, err := composeSetupReaders(*cfg.GitHub)
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	s := &setup{cfg: cfg, options: o, store: store, readers: readers, stdin: stdin, stdout: stdout, stderr: stderr, logger: logger, now: time.Now, random: rand.Reader}
	return s.run(ctx)
}
