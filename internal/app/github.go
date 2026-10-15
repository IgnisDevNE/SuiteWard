package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	// githubBudget is the share of the installation's rate limit this process may use (D-SYNC).
	githubBudget = 0.5
	// githubTimeout bounds one request, so a hung connection cannot hold a cycle until its job ends.
	githubTimeout = 30 * time.Second
	// tickMargin is what a tick leaves of JOB_TIMEOUT to record its last cycles and return.
	tickMargin = 5 * time.Second
)

// repositoryStatus is one entry of the github section of /status.
type repositoryStatus struct {
	Repo        string            `json:"repo"`
	LastSuccess string            `json:"lastSuccess,omitempty"`
	LastError   string            `json:"lastError,omitempty"` // the classification and status, never text GitHub sent
	WaitUntil   string            `json:"waitUntil,omitempty"`
	Protection  *protectionStatus `json:"protection,omitempty"` // absent before the first verification
	Bootstrap   string            `json:"bootstrap,omitempty"`  // classification code of the last bootstrap problem; absent when none
	Checks      string            `json:"checks,omitempty"`     // classification code of the last check job problem; absent when none
	Merges      string            `json:"merges,omitempty"`     // classification code of the last merges job problem; absent when none
	Integrity   string            `json:"integrity,omitempty"`  // verdict of the current canonical (passed, failed, unavailable); absent before the first verification
}

// repositoryReader is the part of the observation store /status reads.
type repositoryReader interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
}

// githubStatus reports what synchronization last saw of the configured repositories.
type githubStatus struct {
	repos     []string
	store     repositoryReader
	ids       *repositoryIDs // learned by the protection check
	problems  *bootstrapProblems
	checks    *bootstrapProblems // the check job's problems, the same in-memory map type
	merges    *bootstrapProblems // the merges job's problems
	integrity *integrityStatus   // nil when the verdicts are not read (a status without an integrity store)
}

// integrityStatus reads the integrity verdict of each enrolled repository's current canonical.
type integrityStatus struct {
	owners ownerReader
	suites governance.UnitOfWork
	store  integrity.Store
}

// verdicts returns the verdict of the current canonical of each enrolled repository, by repository name. A repository
// with no Suite, no canonical, no verification yet or a verdict of another version has none.
func (s *integrityStatus) verdicts(ctx context.Context) (map[string]string, error) {
	verdicts := map[string]string{}
	enrollment, enrolled, err := s.owners.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return verdicts, nil
	}
	for _, project := range enrollment.Projects {
		id := owner.ProjectID(project.RepositoryID)
		var canonical contract.SuiteVersionID
		err := s.suites.Do(ctx, id, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
			state, err := tx.Suite(ctx)
			if err != nil {
				return fmt.Errorf("load suite: %w", err)
			}
			canonical, _ = state.Canonical.Suite().CurrentVersionID()
			return nil
		})
		if errors.Is(err, governance.ErrNotFound) { // no Suite yet
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read the suite of %s: %w", project.Repo, err)
		}
		record, found, err := s.store.Latest(ctx, id, defaultSuite)
		if err != nil {
			return nil, fmt.Errorf("read the integrity verdict of %s: %w", project.Repo, err)
		}
		if canonical != "" && found && record.Version == canonical {
			verdicts[project.Repo] = string(record.Verdict)
		}
	}
	return verdicts, nil
}

// report returns one entry per configured repository, in configuration order;
// a repository that has no cycle yet has only its name.
func (g *githubStatus) report(ctx context.Context, protections map[string]owner.ProtectionResult) ([]repositoryStatus, error) {
	var verdicts map[string]string
	if g.integrity != nil { // absent where no integrity store is composed
		var err error
		if verdicts, err = g.integrity.verdicts(ctx); err != nil {
			return nil, err
		}
	}
	entries := make([]repositoryStatus, 0, len(g.repos))
	for _, repo := range g.repos {
		state, _, err := g.store.Repository(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("read the synchronization state of %s: %w", repo, err)
		}
		entry := repositoryStatus{Repo: repo}
		if !state.LastSuccess.IsZero() {
			entry.LastSuccess = state.LastSuccess.UTC().Format(time.RFC3339)
		}
		entry.LastError = lastErrorText(state)
		entry.Bootstrap = g.problems.lookup(repo)
		entry.Checks = g.checks.lookup(repo)
		entry.Merges = g.merges.lookup(repo)
		entry.Integrity = verdicts[repo]
		if result, verified := protections[repo]; verified {
			entry.Protection = &protectionStatus{
				Result: result.Result, Findings: result.Findings, Caveats: result.Caveats, CheckedAt: result.CheckedAt.UTC().Format(time.RFC3339),
			}
		}
		if !state.WaitUntil.IsZero() {
			entry.WaitUntil = state.WaitUntil.UTC().Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// lastErrorText is the classification of the last failed cycle and its HTTP
// status, never text GitHub sent; empty after a success. /status and the MCP
// status tool both show it.
func lastErrorText(state githubsync.RepositoryState) string {
	text := string(state.LastError)
	if text != "" && state.LastStatus != 0 {
		text += fmt.Sprintf(" (HTTP %d)", state.LastStatus)
	}
	return text
}

// githubSync is the GitHub side of the service, composed only when the GitHub settings are set.
type githubSync struct {
	reconciler *githubsync.Reconciler
	readers    setupReaders   // shared with the protection check: one budget for every token
	writer     *github.Writer // posts the comments, with the comment token
	checkRuns  *github.Checks // posts the check runs, with the checks token
	bot        *botAccount    // SuiteWard's own account id, shared by the comment publisher and the command job
	appID      int64
	repos      []string
	jobTimeout time.Duration
}

// The permission maps of the installation tokens (D-GITHUB-ACCESS): each use
// requests the narrowest one. appauth copies the map it is given.
var (
	// syncPermissions is the read-only map of synchronization.
	syncPermissions = map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read"}
	// protectionPermissions is the map of protection verification.
	protectionPermissions = map[string]string{"metadata": "read", "administration": "read"}
	// commentPermissions is the map of the comment publisher, the only write (the App's Pull requests: write).
	commentPermissions = map[string]string{"metadata": "read", "pull_requests": "write"}
	// checkPermissions is the map of the check run publisher, used for check runs only.
	checkPermissions = map[string]string{"metadata": "read", "checks": "write"}
)

// githubAccess is what every token of the GitHub settings shares: the App, the
// repository names, the HTTP client and the one process-wide budget.
type githubAccess struct {
	settings   config.GitHub
	app        appauth.App
	names      []string
	httpClient *http.Client
	budget     *rest.Budget
}

func newGitHubAccess(g config.GitHub) (githubAccess, error) {
	app, err := appauth.NewApp(g.AppID, []byte(g.AppKey.Reveal()), time.Now)
	if err != nil {
		return githubAccess{}, fmt.Errorf("compose the GitHub App: %w", err)
	}
	names := make([]string, len(g.Repositories))
	for i, repo := range g.Repositories {
		_, names[i], _ = strings.Cut(repo, "/") // the settings guarantee owner/name; the installation takes the name only
	}
	budget, err := rest.NewBudget(githubBudget, time.Now)
	if err != nil {
		return githubAccess{}, fmt.Errorf("compose the GitHub budget: %w", err)
	}
	return githubAccess{settings: g, app: app, names: names, httpClient: &http.Client{Timeout: githubTimeout}, budget: budget}, nil
}

// client builds the REST client of a token narrowed to permissions.
func (a githubAccess) client(permissions map[string]string) (*rest.Client, error) {
	installation, err := a.app.Installation(a.settings.InstallationID, a.names, permissions, a.httpClient, a.settings.APIURL)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub installation: %w", err)
	}
	client, err := rest.NewClient(func(ctx context.Context) (value, identity string, err error) {
		token, err := installation.Token(ctx)
		if err != nil {
			return "", "", err
		}
		return token.Reveal(), token.Identity(), nil
	}, a.httpClient, a.settings.APIURL, a.budget)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub client: %w", err)
	}
	return client, nil
}

// reader builds the reader of a token narrowed to permissions.
func (a githubAccess) reader(permissions map[string]string) (*github.Reader, error) {
	client, err := a.client(permissions)
	if err != nil {
		return nil, err
	}
	reader, err := github.NewReader(client)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub reader: %w", err)
	}
	return reader, nil
}

// writer builds the comment writer of a token narrowed to permissions.
func (a githubAccess) writer(permissions map[string]string) (*github.Writer, error) {
	client, err := a.client(permissions)
	if err != nil {
		return nil, err
	}
	writer, err := github.NewWriter(client)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub writer: %w", err)
	}
	return writer, nil
}

// checkRuns builds the check run writer of a token narrowed to permissions.
func (a githubAccess) checkRuns(permissions map[string]string) (*github.Checks, error) {
	client, err := a.client(permissions)
	if err != nil {
		return nil, err
	}
	runs, err := github.NewChecks(client)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub check runs: %w", err)
	}
	return runs, nil
}

// composeGitHub builds the App installation, the REST clients with the
// process-wide budget and the readers of the GitHub settings, and the
// reconciler over store. Synchronization is read-only: the token requests the
// metadata, pull requests and contents permissions at read level; the
// protection check uses the other reader (Metadata and Administration read).
func composeGitHub(g config.GitHub, jobTimeout time.Duration, store githubsync.Store) (*githubSync, error) {
	access, err := newGitHubAccess(g) // one access, so one budget, for every token
	if err != nil {
		return nil, err
	}
	read, err := access.reader(syncPermissions)
	if err != nil {
		return nil, err
	}
	protect, err := access.reader(protectionPermissions)
	if err != nil {
		return nil, err
	}
	readers := setupReaders{read: read, protect: protect}
	writer, err := access.writer(commentPermissions)
	if err != nil {
		return nil, err
	}
	checkRuns, err := access.checkRuns(checkPermissions)
	if err != nil {
		return nil, err
	}
	// The bot's account id is read on first use, so startup does not depend on GitHub being reachable.
	bot := newBotAccount(func(ctx context.Context) (int64, error) {
		identity, err := read.BotIdentity(ctx, access.app, access.httpClient, access.settings.APIURL)
		return identity.ID, err
	})
	reconciler, err := githubsync.NewReconciler(githubReader{readers.read}, store, time.Now, g.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("compose the synchronization: %w", err)
	}
	return &githubSync{reconciler: reconciler, repos: g.Repositories, jobTimeout: jobTimeout, readers: readers, writer: writer, checkRuns: checkRuns, bot: bot, appID: g.AppID}, nil
}

// tick is the github_sync_tick job: one cycle of every configured repository.
func (s *githubSync) tick(ctx context.Context) error {
	return s.reconciler.Tick(ctx, s.repos, tickDeadline(s.jobTimeout))
}

// syncRepo is the github_sync_repo job. A repository that is not configured is
// an error: the job never calls GitHub for it.
func (s *githubSync) syncRepo(ctx context.Context, repo string) error {
	if !slices.Contains(s.repos, repo) {
		return fmt.Errorf("repository %q is not one of the configured repositories", repo)
	}
	return s.reconciler.SyncRepo(ctx, repo)
}

// tickDeadline is how long a tick may start repositories: JOB_TIMEOUT minus
// 5 s, and at least half of JOB_TIMEOUT, which the settings allow to be 1 s.
func tickDeadline(jobTimeout time.Duration) time.Duration {
	return max(jobTimeout-tickMargin, jobTimeout/2)
}

// githubReader adapts the GitHub adapter to the synchronization's Reader port
// and classifies its errors.
type githubReader struct{ reader *github.Reader }

func (r githubReader) OpenPulls(ctx context.Context, repo string) ([]githubsync.ObservedPull, error) {
	pulls, err := r.reader.OpenPulls(ctx, repo)
	if err != nil {
		return nil, classifyReadError(err)
	}
	observed := make([]githubsync.ObservedPull, len(pulls))
	for i, p := range pulls {
		observed[i] = githubsync.ObservedPull{Number: p.Number, HeadSHA: p.HeadSHA, HeadRef: p.HeadRef, BaseRef: p.BaseRef, BaseSHA: p.BaseSHA, UpdatedAt: p.UpdatedAt, HeadRepo: p.HeadRepo}
	}
	return observed, nil
}

func (r githubReader) PullComments(ctx context.Context, repo string, number int) ([]githubsync.ObservedComment, error) {
	// No since: every cycle lists the comments in full, so no local clock decides what to fetch.
	comments, err := r.reader.PullComments(ctx, repo, number, time.Time{})
	if err != nil {
		return nil, classifyReadError(err)
	}
	observed := make([]githubsync.ObservedComment, len(comments))
	for i, c := range comments {
		observed[i] = githubsync.ObservedComment{CommentID: c.ID, AuthorID: c.AuthorID, AuthorLogin: c.AuthorLogin, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Body: c.Body}
	}
	return observed, nil
}

// classifyReadError turns an error of the GitHub adapter into a
// *githubsync.ReadError: the fixed classification, the status where there is
// one and the time to wait for a rate limit or an exhausted budget, never text
// GitHub sent. An error that matches nothing known (a network failure, an
// unreadable body, a malformed token answer) is a transport failure.
func classifyReadError(err error) error { return readFailure(err) }

// readFailure is the classification of classifyReadError as its concrete type.
func readFailure(err error) *githubsync.ReadError {
	var (
		limited   *rest.RateLimitedError
		exhausted *rest.BudgetExhaustedError
		status    *rest.StatusError
		token     *appauth.StatusError
	)
	switch {
	case errors.As(err, &limited):
		return &githubsync.ReadError{Kind: githubsync.FailureRateLimited, Status: limited.Status, WaitUntil: limited.Until}
	case errors.As(err, &exhausted):
		return &githubsync.ReadError{Kind: githubsync.FailureBudgetExhausted, WaitUntil: exhausted.Reset}
	case errors.As(err, &status):
		return statusFailure(status.Status)
	case errors.As(err, &token):
		return statusFailure(token.Status)
	case errors.Is(err, appauth.ErrPermissionsMismatch):
		return &githubsync.ReadError{Kind: githubsync.FailureForbidden}
	case errors.Is(err, github.ErrInvalidResponse):
		return &githubsync.ReadError{Kind: githubsync.FailureDecode}
	default:
		return &githubsync.ReadError{Kind: githubsync.FailureTransport}
	}
}

// statusFailure classifies an HTTP status the way the rest errors do.
func statusFailure(status int) *githubsync.ReadError {
	kind := githubsync.FailureStatus
	switch status {
	case http.StatusUnauthorized:
		kind = githubsync.FailureUnauthorized
	case http.StatusForbidden:
		kind = githubsync.FailureForbidden
	case http.StatusNotFound:
		kind = githubsync.FailureNotFound
	case http.StatusUnprocessableEntity:
		kind = githubsync.FailureUnprocessable
	}
	return &githubsync.ReadError{Kind: kind, Status: status}
}
