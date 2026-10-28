package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// `suiteward check` is the GitHub and database half of `suiteward doctor`, which runs it in the container. It reads
// and posts nothing: GitHub is read with installation tokens and the database with the service's own role. A result
// carries identifiers, logins, repository names and codes, never text GitHub sent, a token, a key or a connection string.

const (
	nameApp           = "github.app"
	nameInstallation  = "github.installation"
	namePermissions   = "github.permissions"
	nameRepositories  = "github.repositories"
	nameProtection    = "protection." // followed by the repository
	nameOwnerEnrolled = "owner.enrolled"
	nameSchema        = "database.schema"
	nameRole          = "database.role"

	appFix = "check SUITEWARD_GITHUB_APP_ID and the private key (SUITEWARD_GITHUB_APP_KEY or its file) against the App's settings page"
	dbFix  = "check SUITEWARD_DATABASE_URL, that the database is running and that the role's password is right"
)

// selfCheckResult is one line of `suiteward check`; the JSON form always has all four keys, for `doctor`.
type selfCheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
	// Warning marks a passing result that needs a look, such as GitHub not being configured; it is in the JSON only when set.
	Warning bool `json:"warning,omitempty"`
}

func checkPassed(name, detail string) selfCheckResult {
	return selfCheckResult{Name: name, OK: true, Detail: detail}
}

func checkFailed(name, detail, fix string) selfCheckResult {
	return selfCheckResult{Name: name, Detail: detail, Fix: fix}
}

// notConfiguredFix is how an instance installed without GitHub gets it.
const notConfiguredFix = "run `suiteward install` again with --github yes (or the GitHub values), then enroll the owner"

// checkNotConfigured is the result of a GitHub check of an instance installed without GitHub: it passes, as a warning.
func checkNotConfigured(name string) selfCheckResult {
	return selfCheckResult{Name: name, OK: true, Warning: true, Detail: "not configured: GitHub is off in this instance", Fix: notConfiguredFix}
}

// checkSkipped is the result of a check that needs another one to pass first.
func checkSkipped(name, dependency string) selfCheckResult {
	return checkFailed(name, "not checked: "+dependency+" failed", "fix "+dependency+" first")
}

// parseSelfCheck reads the flags of `check`. An error has been reported on stderr with the usage.
func parseSelfCheck(args []string, stderr io.Writer) (bool, error) {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, usage) } // a failed write to stderr cannot be reported anywhere else
	asJSON := flags.Bool("json", false, "print the results as a JSON array of {name, ok, detail, fix}")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() > 0 {
		err := fmt.Errorf("unexpected argument %q", flags.Arg(0))
		reportf(stderr, "%v\n", err)
		_, _ = io.WriteString(stderr, usage) // a failed write to stderr cannot be reported anywhere else
		return false, err
	}
	return *asJSON, nil
}

// runSelfCheck runs every check, prints the results and returns the exit code: 0 when none fails (a warning is not a failure), 1 otherwise.
// The database is checked first because the protection check needs the test check of the enrollment.
func runSelfCheck(ctx context.Context, cfg config.Config, asJSON bool, stdout, stderr io.Writer) int {
	schema, role, enrolled, projects := selfCheckDatabaseResults(ctx, cfg)
	var results []selfCheckResult
	if cfg.GitHub == nil { // the install allows an instance without GitHub: its checks are warnings, the database ones still run
		for _, name := range []string{nameApp, nameInstallation, namePermissions, nameRepositories} {
			results = append(results, checkNotConfigured(name))
		}
	} else {
		results = selfCheckGitHubResults(ctx, *cfg.GitHub, projects)
	}
	results = append(results, enrolled, schema, role)
	if err := writeSelfCheck(stdout, results, asJSON); err != nil {
		reportf(stderr, "write output: %v\n", err)
		return 1
	}
	if slices.ContainsFunc(results, func(r selfCheckResult) bool { return !r.OK }) {
		return 1
	}
	return 0
}

// writeSelfCheck prints `ok <name>`, `WARN <name>: <detail> — <fix>` or `FAIL <name>: <detail> — <fix>` per result, or the JSON array.
func writeSelfCheck(w io.Writer, results []selfCheckResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(results)
	}
	var out strings.Builder
	for _, r := range results {
		switch {
		case r.OK && r.Warning:
			out.WriteString("WARN " + r.Name + ": " + r.Detail + " — " + r.Fix + "\n")
		case r.OK:
			out.WriteString("ok " + r.Name + "\n")
		default:
			out.WriteString("FAIL " + r.Name + ": " + r.Detail + " — " + r.Fix + "\n")
		}
	}
	_, err := io.WriteString(w, out.String())
	return err
}

// readDetail is the classification of a failed GitHub read and its HTTP status, never text GitHub sent.
func readDetail(err error) string {
	switch {
	case errors.Is(err, appauth.ErrMalformedAnswer):
		return "GitHub's answer was malformed"
	case errors.Is(err, appauth.ErrTooManyEntries):
		return fmt.Sprintf("more than %d entries", appauth.MaxListed)
	}
	failure := readFailure(err)
	if failure.Status == 0 {
		return string(failure.Kind)
	}
	return fmt.Sprintf("%s (HTTP %d)", failure.Kind, failure.Status)
}

// selfCheckGitHubResults runs the GitHub checks in order; a check that needs an earlier one is reported as not checked
// when that one fails. projects are the enrolled repositories, which hold the test checks; none before setup.
func selfCheckGitHubResults(ctx context.Context, g config.GitHub, projects []owner.ProjectPolicy) []selfCheckResult {
	protections := make([]string, len(g.Repositories))
	for i, repo := range g.Repositories {
		protections[i] = nameProtection + repo
	}
	skipFrom := func(results []selfCheckResult, dependency string, names ...string) []selfCheckResult {
		for _, name := range names {
			results = append(results, checkSkipped(name, dependency))
		}
		return results
	}

	access, result := selfCheckApp(ctx, g)
	results := []selfCheckResult{result}
	if !result.OK {
		return skipFrom(results, nameApp, append([]string{nameInstallation, namePermissions, nameRepositories}, protections...)...)
	}
	installed, result := selfCheckInstallation(ctx, g, access)
	results = append(results, result)
	if !result.OK {
		return skipFrom(results, nameInstallation, append([]string{namePermissions, nameRepositories}, protections...)...)
	}
	repositories := selfCheckRepositories(ctx, g, access)
	if repositories.covered {
		results = append(results, selfCheckPermissions(ctx, g, access, installed))
	} else { // GitHub refuses every token that names a repository outside the installation
		results = append(results, checkSkipped(namePermissions, nameRepositories))
	}
	results = append(results, repositories.result)
	for _, name := range g.Repositories {
		repo, readable := repositories.found[name]
		if !readable {
			results = append(results, checkSkipped(nameProtection+name, nameRepositories))
			continue
		}
		testCheck := "" // none before setup, which the protection result then says
		for _, project := range projects {
			if project.RepositoryID == repo.ID {
				testCheck = project.TestCheck
			}
		}
		evaluated, _ := evaluateProtection(ctx, slog.New(slog.DiscardHandler), repositories.readers.protect, g.AppID, name, repo, testCheck) // a wait is a failure here
		results = append(results, selfCheckProtection(name, evaluated))
	}
	return results
}

// selfCheckApp checks that the App JWT works (GET /app).
func selfCheckApp(ctx context.Context, g config.GitHub) (githubAccess, selfCheckResult) {
	access, err := newGitHubAccess(g)
	if err != nil {
		return githubAccess{}, checkFailed(nameApp, err.Error(), appFix) // the key's format is named, never the key
	}
	slug, err := access.app.Slug(ctx, access.httpClient, g.APIURL)
	if err != nil {
		return githubAccess{}, checkFailed(nameApp, "GET /app failed: "+readDetail(err), appFix)
	}
	return access, checkPassed(nameApp, "app "+slug)
}

// selfCheckInstallation checks that the configured installation is one of the App's.
func selfCheckInstallation(ctx context.Context, g config.GitHub, access githubAccess) (appauth.InstallationInfo, selfCheckResult) {
	installations, err := access.app.Installations(ctx, access.httpClient, g.APIURL)
	if err != nil {
		return appauth.InstallationInfo{}, checkFailed(nameInstallation, "the App's installations could not be listed: "+readDetail(err), appFix)
	}
	ids := make([]string, len(installations))
	for i, installation := range installations {
		if installation.ID == g.InstallationID {
			return installation, checkPassed(nameInstallation, fmt.Sprintf("installation %d on %s (%s, %s repositories)", installation.ID, installation.Account, installation.AccountType, installation.RepositorySelection))
		}
		ids[i] = strconv.FormatInt(installation.ID, 10)
	}
	listed := "the App has none"
	if len(ids) > 0 {
		listed = "the App has " + strings.Join(ids, ", ")
	}
	return appauth.InstallationInfo{}, checkFailed(nameInstallation, fmt.Sprintf("installation %d is not one of the App's installations: %s", g.InstallationID, listed),
		"install the App on the account, or set SUITEWARD_GITHUB_INSTALLATION_ID to the number in the address of the installation's settings page")
}

// selfCheckTokens are the installation tokens the service mints, one per use (D-GITHUB-ACCESS).
var selfCheckTokens = []struct {
	use         string
	permissions map[string]string
}{
	{"synchronization", syncPermissions}, {"protection", protectionPermissions}, {"comment", commentPermissions}, {"check run", checkPermissions},
	{"archive", archivePermissions},
}

// permissionRank orders the levels of a GitHub permission; an unknown level is below read.
func permissionRank(level string) int {
	switch level {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// selfCheckPermissions checks that every token the service uses mints with exactly the permissions it asks for, and that
// the installation grants nothing more than those tokens use (the install guide: exactly the five, and nothing else).
func selfCheckPermissions(ctx context.Context, g config.GitHub, access githubAccess, installed appauth.InstallationInfo) selfCheckResult {
	var problems, mismatches []string
	missing := map[string]bool{}
	required := map[string]string{} // the highest level any token asks for
	for _, token := range selfCheckTokens {
		for name, level := range token.permissions {
			if permissionRank(level) > permissionRank(required[name]) {
				required[name] = level
			}
		}
		installation, err := access.app.Installation(g.InstallationID, access.names, token.permissions, access.httpClient, g.APIURL)
		if err == nil {
			_, err = installation.Token(ctx)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, appauth.ErrPermissionsMismatch) { // GitHub's answer is not printed, SuiteWard's own request is
			problems = append(problems, fmt.Sprintf("the %s token was granted other permissions than the ones it requests", token.use))
			mismatches = append(mismatches, fmt.Sprintf("the %s token requests %s", token.use, formatPermissions(token.permissions)))
		} else {
			problems = append(problems, fmt.Sprintf("the %s token cannot be minted: %s", token.use, readDetail(err)))
		}
		for name, level := range token.permissions {
			if permissionRank(installed.Permissions[name]) < permissionRank(level) {
				missing[name+": "+level] = true
			}
		}
	}
	var excess []string
	for name, level := range installed.Permissions {
		if permissionRank(level) > permissionRank(required[name]) {
			excess = append(excess, name+": "+level)
		}
	}
	slices.Sort(excess)
	if len(problems) == 0 && len(excess) == 0 {
		return checkPassed(namePermissions, "every token mints with its exact permissions and the App grants nothing more")
	}
	var detail, fix []string
	detail = append(detail, problems...)
	if names := slices.Sorted(maps.Keys(missing)); len(names) > 0 {
		detail = append(detail, "the App lacks "+strings.Join(names, ", "))
		fix = append(fix, "grant the App "+strings.Join(names, ", ")+" in its settings, then accept the new permissions on the installation")
	}
	if len(excess) > 0 {
		detail = append(detail, "the App grants more than SuiteWard uses: "+strings.Join(excess, ", "))
		fix = append(fix, "remove "+strings.Join(excess, ", ")+" from the App's permissions: SuiteWard needs exactly the five of the install guide")
	}
	if len(mismatches) > 0 {
		fix = append(fix, "compare the App's permissions with the install guide ("+strings.Join(mismatches, "; ")+")")
	}
	if len(fix) == 0 {
		fix = append(fix, "compare the App's repository permissions with the install guide")
	}
	return checkFailed(namePermissions, strings.Join(detail, "; "), strings.Join(fix, "; "))
}

// repositoryReads is what selfCheckRepositories found: the readers and the repositories read, by configured name, and
// whether the installation covers every configured repository.
type repositoryReads struct {
	result  selfCheckResult
	readers setupReaders
	found   map[string]github.Repository
	covered bool
}

// selfCheckRepositories checks that the installation covers every configured repository and reads the covered ones.
// A token that names an uncovered repository is refused by GitHub, so the readers ask for the covered ones only.
func selfCheckRepositories(ctx context.Context, g config.GitHub, access githubAccess) repositoryReads {
	listed, err := access.app.InstallationRepositories(ctx, g.InstallationID, access.httpClient, g.APIURL)
	if errors.Is(err, appauth.ErrTooManyEntries) {
		return repositoryReads{result: checkFailed(nameRepositories, fmt.Sprintf("the installation has more than %d repositories, more than check can list", appauth.MaxListed),
			"install the App on selected repositories, at most the 20 SuiteWard supports, instead of all of them")}
	}
	if err != nil {
		return repositoryReads{result: checkFailed(nameRepositories, "the installation's repositories could not be listed: "+readDetail(err), appFix)}
	}
	var uncovered []string
	covered := g
	covered.Repositories = nil
	for _, name := range g.Repositories {
		if slices.ContainsFunc(listed, func(full string) bool { return strings.EqualFold(full, name) }) { // GitHub names are case-insensitive
			covered.Repositories = append(covered.Repositories, name)
		} else {
			uncovered = append(uncovered, name)
		}
	}
	var problems []string
	if len(uncovered) > 0 {
		problems = append(problems, "not covered by the installation: "+strings.Join(uncovered, ", "))
	}
	reads := repositoryReads{found: make(map[string]github.Repository, len(covered.Repositories)), covered: len(uncovered) == 0}
	if len(covered.Repositories) > 0 {
		if reads.readers, err = composeSetupReaders(covered); err != nil {
			reads.result = checkFailed(nameRepositories, err.Error(), appFix)
			return reads
		}
		for _, name := range covered.Repositories {
			repo, err := reads.readers.read.Repository(ctx, name)
			if err != nil {
				problems = append(problems, name+" is not readable: "+readDetail(err))
				continue
			}
			reads.found[name] = repo
		}
	}
	if len(problems) > 0 {
		reads.result = checkFailed(nameRepositories, strings.Join(problems, "; "),
			"add the repositories to the installation's repository access, or remove them from SUITEWARD_GITHUB_REPOSITORIES, and make sure each is spelled OWNER/NAME")
		return reads
	}
	reads.result = checkPassed(nameRepositories, strings.Join(g.Repositories, ", ")+" covered and readable")
	return reads
}

// selfCheckProtection is the result of one repository's protection: enforced passes; findings, enforcement-unavailable
// and unknown fail with the evaluator's fixed instructions, which carry the fix.
func selfCheckProtection(repo string, result protection.Result) selfCheckResult {
	name := nameProtection + repo
	instructions := strings.Join(result.Instructions, " ")
	switch result.Kind {
	case protection.KindEnforced:
		detail := string(result.Kind)
		if caveats := codeStrings(result.Caveats); len(caveats) > 0 {
			detail += " (caveats: " + strings.Join(caveats, ", ") + ")"
		}
		return checkPassed(name, detail)
	case protection.KindFindings:
		return checkFailed(name, string(result.Kind)+": "+strings.Join(codeStrings(result.Findings), ", "), instructions)
	default: // enforcement-unavailable and unknown name themselves
		return checkFailed(name, string(result.Kind), instructions)
	}
}

// neverWrites stands in for the artifact verifier and the job inserter of the store: check only reads, and the store
// touches them only when it writes. It also keeps check away from the artifact directory, which a running service owns.
type neverWrites struct{}

var errNeverWrites = errors.New("check never writes")

func (neverWrites) Verify(context.Context, artifact.Digest) error { return errNeverWrites }

func (neverWrites) Insert(context.Context, pgx.Tx, governance.Job) (int64, error) {
	return 0, errNeverWrites
}

// selfCheckDatabaseResults checks the schema version, the connected role and the enrollment, and returns the enrolled
// projects. The role is checked even when the schema is not ready; the enrollment needs the schema.
func selfCheckDatabaseResults(ctx context.Context, cfg config.Config) (schema, role, enrolled selfCheckResult, projects []owner.ProjectPolicy) {
	ctx, cancel := context.WithTimeout(ctx, selfCheckDatabaseTimeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL.Reveal())
	if err != nil { // the cause is dropped: pgx can echo part of the connection string
		down := func(name string) selfCheckResult {
			return checkFailed(name, "cannot open the database connection", dbFix)
		}
		return down(nameSchema), down(nameRole), down(nameOwnerEnrolled), nil
	}
	defer pool.Close()
	role = selfCheckRole(ctx, pool)
	var store *postgres.Store
	if err = ctx.Err(); err == nil { // NewStore has a clock of its own and ignores ctx: it is not started once the time is up
		store, err = postgres.NewStore(pool, neverWrites{}, neverWrites{})
	}
	switch {
	case errors.Is(err, postgres.ErrSchemaNewer):
		schema = checkFailed(nameSchema, "the database was migrated by a newer release than this binary", "run the newer release, or restore a backup taken before it")
	case errors.Is(err, postgres.ErrSchemaNotReady):
		schema = checkFailed(nameSchema, fmt.Sprintf("the schema is not at version %d", migrations.SupportedVersion), "run `suiteward migrate` with the migration settings, then check again")
	case err != nil: // the cause is dropped: it can name the host and the user
		schema = checkFailed(nameSchema, "cannot reach the database", dbFix)
	}
	if err != nil {
		return schema, role, checkSkipped(nameOwnerEnrolled, nameSchema), nil
	}
	schema = checkPassed(nameSchema, fmt.Sprintf("schema version %d", migrations.SupportedVersion))
	state, found, err := store.Current(ctx)
	switch {
	case err != nil:
		return schema, role, checkFailed(nameOwnerEnrolled, "the enrollment could not be read", dbFix), nil
	case !found && cfg.GitHub == nil: // setup needs GitHub, so without it no owner can be enrolled
		warning := checkNotConfigured(nameOwnerEnrolled)
		warning.Detail = "no owner is enrolled, and setup needs GitHub, which is not configured"
		return schema, role, warning, nil
	case !found:
		return schema, role, checkFailed(nameOwnerEnrolled, "no owner is enrolled",
			"run `suiteward enroll` where you ran `suiteward install` (as `suiteward` on an isolated host), on a real terminal with session recording off (see the install guide)"), nil
	}
	return schema, role, checkPassed(nameOwnerEnrolled, fmt.Sprintf("%s is enrolled for %d repositories, tier %s", state.Owner.Login, len(state.Projects), state.Tier)), state.Projects
}

// selfCheckRole checks that the connected role cannot undo what only the owner may do: it is not a superuser and it
// cannot create roles or bypass row-level security.
func selfCheckRole(ctx context.Context, pool *pgxpool.Pool) selfCheckResult {
	var name string
	var superuser, createRole, bypassRLS bool
	err := pool.QueryRow(ctx, `SELECT current_user, rolsuper, rolcreaterole, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&name, &superuser, &createRole, &bypassRLS)
	if err != nil { // the cause is dropped: it can name the host and the user
		return checkFailed(nameRole, "cannot read the connected role", dbFix)
	}
	var reasons []string
	if superuser {
		reasons = append(reasons, "is a superuser")
	}
	if createRole {
		reasons = append(reasons, "can create roles")
	}
	if bypassRLS {
		reasons = append(reasons, "bypasses row-level security")
	}
	if len(reasons) > 0 {
		return checkFailed(nameRole, fmt.Sprintf("role %s %s", name, strings.Join(reasons, ", ")),
			"connect the service as the least-privilege application role that `suiteward migrate` grants, never as the owner or a superuser")
	}
	return checkPassed(nameRole, "role "+name+" is not a superuser and cannot create roles or bypass row-level security")
}

// formatPermissions renders a permission map in name order, for the fix.
func formatPermissions(permissions map[string]string) string {
	names := slices.Sorted(maps.Keys(permissions))
	for i, name := range names {
		names[i] = name + ": " + permissions[name]
	}
	return strings.Join(names, ", ")
}

// selfCheckDatabaseTimeout bounds the database checks together, so a host that never answers cannot hang `check`.
var selfCheckDatabaseTimeout = 15 * time.Second
