package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	appowner "github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// resolveGitHub is the one entry the tier flows call (M1.12-I6):
//
//	func resolveGitHub(ctx context.Context, env githubEnv) (GitHubResult, error)
//
// It makes sure the instance has a GitHub App, an installation and the chosen repositories, and leaves the repositories
// protected or says why not. Order: the App (given, or created through the manifest flow), the installation, the
// repositories (1 to MaxRepositories, one owner), the owner login, the test check per repository, a ruleset JSON per
// repository, and a re-read of the protection. It prints to env.Out and asks through env.R.
//
// The private key is returned in GitHubResult.PrivateKey for the caller to put into the SecretStore. Nothing here writes it
// to disk, prints it or puts it in an error. An App given through flags or answers needs "app-id", "installation-id" and the
// key: "app-key-file" (a path, or - for stdin) or "app-key" (the answers file, a no-echo prompt, or stdin; when stdin carries the key, every
// other answer must come from flags or the answers file, because reading the key consumes the rest of stdin).
//
// Answer keys (flag names and answers-file keys): github-account (personal, or an organization login), manifest-code (the answers file only: it is a credential for an hour),
// app-id, installation-id, app-key-file, app-key, repositories, owner, test-check (a plain check name for one repository,
// or OWNER/NAME=CHECK entries one per line as setup takes them; test-check.OWNER/NAME for one repository wins), ruleset-imported.
//
// Without an app-id answer, githubEnv.Existing (the settings' GitHub part) with KeyExists and ReadKey reuses its App when the
// key secret exists; otherwise the App is created and githubEnv.StoreKey, when set, receives the key right after the
// conversion, so the flow does not depend on keeping it in memory, and githubEnv.RecordApp, when set, records the App (never the key)
// right after that. A re-run whose githubEnv.Pending holds that record, and which has no App from an answer or the settings, takes
// the App up (its key secret is reused, and it waits for or finds the installation) instead of creating a second one.
func resolveGitHub(ctx context.Context, env githubEnv) (GitHubResult, error) {
	env, err := env.withDefaults()
	if err != nil {
		return GitHubResult{}, err
	}
	app, res, created, err := chooseApp(ctx, env)
	if err != nil {
		return GitHubResult{}, createdError(err, created)
	}
	// A failure after the App was created leaves an App that is used only when the run is repeated: the error says how.
	fail := func(err error) (GitHubResult, error) { return GitHubResult{}, createdError(err, created) }

	if res.Repositories, err = chooseRepositories(ctx, env, app, res); err != nil {
		return fail(err)
	}
	defaultOwner := ""
	if res.AccountType == "User" {
		defaultOwner = res.Account
	}
	if res.OwnerLogin, err = env.R.Value("owner", "GitHub login of the instance owner, the only account whose approvals count", defaultOwner); err != nil {
		return fail(err)
	}
	if !ghLoginPattern.MatchString(res.OwnerLogin) {
		return fail(fmt.Errorf("owner %q is not a GitHub login", res.OwnerLogin))
	}

	budget, err := rest.NewBudget(githubBudget, env.Now)
	if err != nil {
		return fail(fmt.Errorf("compose the GitHub budget: %w", err))
	}
	access := ghAccess{app: app, id: res.InstallationID, names: repositoryNames(res.Repositories), http: env.HTTP, api: env.APIURL, budget: budget}
	repos, err := readRepositories(ctx, access, res.Repositories)
	if err != nil {
		return fail(err)
	}
	if res.TestChecks, err = chooseTestChecks(ctx, env, access, repos); err != nil {
		return fail(err)
	}
	if res.Protection, err = importRulesets(ctx, env, access, res, repos); err != nil {
		return fail(err)
	}
	if res.replaced != "" {
		ghSay(env.Out, "\n%s", res.replaced)
	}
	return res, nil
}

// chooseApp finds the App the flow works with: the one an answer names, else a new one when --github new-app asks for it, else
// the one an earlier run created and recorded (PendingApp) when it is not the App of the settings, else the one the settings name,
// else a new one. The record wins over the settings because a run that made a new App stored its key and recorded it: that key is
// the secret now, and the settings' App must not run with it. created is the page that deletes the App, when one was created by
// this run or an earlier one.
func chooseApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, created string, err error) {
	if _, ok := env.R.given("app-id"); ok { // an answer wins over the settings
		app, res, err = existingApp(ctx, env)
		return app, res, "", err
	}
	if wantsNewApp(env.R) {
		return replaceApp(ctx, env)
	}
	if recordWins(env.Existing, env.Pending) {
		app, res, created, err = resumeApp(ctx, env)
		switch {
		case errors.Is(err, errNewApp):
			return replaceApp(ctx, env)
		case err != nil && !errors.Is(err, errPendingKeyMissing):
			err = fmt.Errorf("%w: %w", errResuming, err)
		}
		return app, res, created, err
	}
	app, res, reused, err := settingsApp(ctx, env)
	switch {
	case errors.Is(err, errNewApp):
		return replaceApp(ctx, env)
	case err != nil || reused:
		return app, res, "", err
	}
	return createApp(ctx, env)
}

// recordWins says that the App an earlier run recorded is not the App of the settings.
func recordWins(existing *GitHub, pending *PendingApp) bool {
	return pending != nil && (existing == nil || existing.AppID != pending.AppID)
}

// newAppAnswer is the value of --github that creates a new App although the settings or an earlier run name one.
const newAppAnswer = "new-app"

// errNewApp is the choice of a new App over the key file of the App that has no key secret.
var errNewApp = errors.New("a new App was chosen")

// errNewAppKeyExists means --github new-app found a key secret: the key of the new App cannot go over it.
var errNewAppKeyExists = errors.New("--github new-app needs the key secret to be free")

// replaceApp creates a new App although one is named. It names the old App (or Apps) first, so that a run that stops after the new
// one exists leaves the user with the page that deletes the old one, and again at the end of the flow. It refuses before an App is
// created when the key secret exists: the new key is never stored over the old one.
func replaceApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, created string, err error) {
	if env.KeyExists != nil {
		have, err := env.KeyExists(ctx)
		if err != nil {
			return app, res, "", fmt.Errorf("look for the App key secret: %w", err)
		}
		if have {
			return app, res, "", fmt.Errorf("%w: %s", errNewAppKeyExists, keyInTheWay(env))
		}
	}
	note := replacedNote(env)
	if note != "" {
		ghSay(env.Out, "%s", note)
	}
	app, res, created, err = createApp(ctx, env)
	res.replaced = note
	return app, res, created, err
}

// keyInTheWay says whose key the secret holds, which is the record's when a record names another App than the settings.
func keyInTheWay(env githubEnv) string {
	switch {
	case recordWins(env.Existing, env.Pending):
		p := env.Pending
		return fmt.Sprintf("the secret %s holds the key of the App %s that an earlier run created and recorded: run again without --github new-app to take that App up; to replace it instead, remove the secret and the record, and delete the App at %s",
			SecretGitHubAppKey, p.Slug, appPage(env.WebURL, p.Slug, p.Account, p.AccountType))
	case env.Existing != nil && env.Existing.AppID != 0:
		return fmt.Sprintf("the secret %s holds the key of the App %d of the settings, and the key of a new App cannot be stored over it. Remove it, then run again: the App %d stops working on this instance, and stays on GitHub (delete it at %s)",
			SecretGitHubAppKey, env.Existing.AppID, env.Existing.AppID, appListPages(env.WebURL, *env.Existing))
	}
	return fmt.Sprintf("the secret %s exists, and the key of a new App cannot be stored over it: remove it, then run again", SecretGitHubAppKey)
}

// replacedNote says that the App the settings named, and the App an earlier run recorded, stay on GitHub, and where each is deleted.
func replacedNote(env githubEnv) string {
	var note string
	if env.Existing != nil && env.Existing.AppID != 0 {
		note = fmt.Sprintf("The App %d that the settings named stays on GitHub. Delete it at %s.\n", env.Existing.AppID, appListPages(env.WebURL, *env.Existing))
	}
	if p := env.Pending; p != nil && (env.Existing == nil || env.Existing.AppID != p.AppID) {
		note += fmt.Sprintf("The App %s that an earlier run recorded stays on GitHub. Delete it at %s; the record is replaced.\n", p.Slug, appPage(env.WebURL, p.Slug, p.Account, p.AccountType))
	}
	return note
}

// appListPages are the pages that list the Apps of an account: the settings only have the App's id, not its slug or its owner.
func appListPages(webURL string, gh GitHub) string {
	return fmt.Sprintf("%s/settings/apps (or, if an organization owns it, %s/organizations/%s/settings/apps), the App with the ID %d", webURL, webURL, gh.Owner, gh.AppID)
}

// missingKey is how an App without its key secret gets a key: the key file given, else on a terminal the choice of the key file
// or a new App (errNewApp). Without a terminal, and with no key file, it stops and names both routes. who names the App and
// deleteAt says where the old App is deleted when a new one is made.
func missingKey(env githubEnv, who, deleteAt string) ([]byte, error) {
	_, haveFile := env.R.given("app-key-file")
	if _, haveKey := env.R.given("app-key"); haveFile || haveKey {
		return readAppKey(env.R)
	}
	if !env.R.term.IsTerminal {
		return nil, fmt.Errorf("the %s has no key in the secret store (%s): pass --github-app-key-file <file> with the App's private key (create one on the App's settings page if you have none), or --github %s to create a new App (the old one stays on GitHub: delete it at %s)",
			who, SecretGitHubAppKey, newAppAnswer, deleteAt)
	}
	ghSay(env.Out, "The %s has no key in the secret store (%s).\n  1) give the App's private key file\n  2) create a new App (the old one stays on GitHub: delete it at %s)\n", who, SecretGitHubAppKey, deleteAt)
	choice, err := env.R.ask("Choose 1 or 2: ")
	if err != nil {
		return nil, fmt.Errorf("read the choice: %w", err)
	}
	switch choice {
	case "1":
		return readAppKey(env.R)
	case "2":
		return nil, errNewApp
	}
	return nil, fmt.Errorf("%q is not 1 or 2", choice)
}

// createdError says how to start over when the App was created by this run or an earlier one.
func createdError(err error, deleteURL string) error {
	if deleteURL == "" {
		return err
	}
	return fmt.Errorf("%w (the GitHub App was created by this run or an earlier one: if you cannot continue, delete it at %s)", err, deleteURL)
}

// appPage is the settings page of an App, where it is deleted.
func appPage(webURL, slug, ownerLogin, ownerType string) string {
	if ownerType == "Organization" {
		return webURL + "/organizations/" + ownerLogin + "/settings/apps/" + slug
	}
	return webURL + "/settings/apps/" + slug
}

// githubBudget is the fraction of the rate limit the installer may use; it matches the service's.
const githubBudget = 0.5

// githubEnv is what resolveGitHub needs from the machine. A zero duration, APIURL, WebURL, HTTP or Now takes the production default.
type githubEnv struct {
	R               *Resolver
	Out             io.Writer
	HTTP            *http.Client
	APIURL, WebURL  string // https://api.github.com and https://github.com
	Now             func() time.Time
	Tier            config.Tier
	SettingsDir     string // where the ruleset files go
	ManifestPort    int    // 0: any free port; --manifest-port for the isolated SSM port forward
	SkipRulesetWait bool
	ManifestWait    time.Duration // the manifest code, 15 minutes
	InstallWait     time.Duration // the installation, 10 minutes
	PollEvery       time.Duration // 5 seconds

	// StoreKey puts a key the flow created into the secret store, right after the conversion and before anything else can fail.
	StoreKey func(ctx context.Context, key []byte) error
	// RecordApp records the App the flow created, right after its key is stored, so that a run that stops before the flow ends
	// can take the App up again; it never receives the key.
	RecordApp func(ctx context.Context, app PendingApp) error
	// Pending is the App an earlier run recorded and did not finish with: with no App named by an answer or the settings, the
	// flow takes it up (its key secret must exist, see KeyExists) instead of creating another.
	Pending *PendingApp
	// Existing is the GitHub part of the settings. With an App id and an installation id, an App whose key secret exists
	// (KeyExists) is reused instead of created; ReadKey reads that secret to verify the App.
	Existing  *GitHub
	KeyExists func(ctx context.Context) (bool, error)
	ReadKey   func(ctx context.Context) ([]byte, error)
}

func (e githubEnv) withDefaults() (githubEnv, error) {
	if e.R == nil || e.Out == nil || e.SettingsDir == "" {
		return githubEnv{}, errors.New("the GitHub flow needs a resolver, an output and the settings directory")
	}
	if e.HTTP == nil {
		e.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if e.APIURL == "" {
		e.APIURL = "https://api.github.com"
	}
	if e.WebURL == "" {
		e.WebURL = "https://github.com"
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.ManifestWait == 0 {
		e.ManifestWait = defaultManifestWait
	}
	if e.InstallWait == 0 {
		e.InstallWait = 10 * time.Minute
	}
	if e.PollEvery == 0 {
		e.PollEvery = 5 * time.Second
	}
	e.WebURL = strings.TrimSuffix(e.WebURL, "/")
	return e, nil
}

// GitHubResult is what the install flow learned from GitHub.
type GitHubResult struct {
	AppID          int64
	Slug           string
	InstallationID int64
	Account        string // the account the App is installed on, and the owner of every repository
	AccountType    string // User or Organization
	OwnerLogin     string // the instance owner, for setup
	Repositories   []string
	TestChecks     map[string]string            // repository -> test check
	Protection     map[string]protection.Result // repository -> the result of the re-read
	PrivateKey     []byte                       `json:"-" yaml:"-"` // in memory only, for the SecretStore; no encoder may carry it
	replaced       string                       // says that the App this one replaces stays on GitHub; printed at the end of the flow
}

// Format never renders the private key, whatever the verb.
func (r GitHubResult) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "GitHubResult{app %d, installation %d, %s, %v}", r.AppID, r.InstallationID, r.Account, r.Repositories)
}

// given returns the flag or answers-file value of key without ever asking.
func (r *Resolver) given(key string) (string, bool) {
	if v, ok := r.flags[key]; ok {
		return v, true
	}
	v, ok := r.answers[key]
	return v, ok
}

// existingApp verifies an App that was made earlier: its JWT works (GET /app) and the installation is one of its own.
func existingApp(ctx context.Context, env githubEnv) (appauth.App, GitHubResult, error) {
	idText, _ := env.R.given("app-id")
	appID, err := strconv.ParseInt(strings.TrimSpace(idText), 10, 64)
	if err != nil || appID <= 0 {
		return appauth.App{}, GitHubResult{}, fmt.Errorf("app-id %q is not a positive number", idText)
	}
	instText, ok := env.R.given("installation-id")
	if !ok {
		return appauth.App{}, GitHubResult{}, errors.New("installation-id is required with app-id: pass --installation-id (the number in the address of the installation's settings page)")
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(instText), 10, 64)
	if err != nil || installationID <= 0 {
		return appauth.App{}, GitHubResult{}, fmt.Errorf("installation-id %q is not a positive number", instText)
	}
	key, err := readAppKey(env.R)
	if err != nil {
		return appauth.App{}, GitHubResult{}, err
	}
	return verifyApp(ctx, env, appID, installationID, key)
}

// readAppKey reads the PEM of an App that exists: from the file of "app-key-file" (- is standard input), else from "app-key"
// (the answers file, or stdin), else, at a terminal, from a file whose path it asks for: a prompt that reads one line cannot
// take a pasted PEM.
func readAppKey(r *Resolver) ([]byte, error) {
	path, haveFile := r.given("app-key-file")
	if _, haveKey := r.given("app-key"); !haveFile && !haveKey && r.term.IsTerminal {
		var err error
		if path, err = r.Value("app-key-file", "Path of the App's private key file (.pem)", ""); err != nil {
			return nil, err
		}
		haveFile = true
	}
	if !haveFile {
		return r.Secret("app-key", "GitHub App private key (PEM)")
	}
	var key []byte
	var err error
	if path == "-" { // standard input, as the flag says
		key, err = io.ReadAll(r.in)
	} else {
		key, err = os.ReadFile(filepath.Clean(path))
	}
	if err != nil {
		return nil, fmt.Errorf("read the App key file: %w", err)
	}
	return key, nil
}

// settingsApp reuses the App the settings name when its key secret exists, or when the key is given again (missingKey).
// found is false when the settings name no App. An App whose key cannot be looked for or read is an error, never a new App, and
// errNewApp says that a new App was chosen over the key.
func settingsApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, found bool, err error) {
	e := env.Existing
	if e == nil || e.AppID == 0 {
		return app, res, false, nil
	}
	if e.InstallationID <= 0 {
		return app, res, false, fmt.Errorf("the settings name the App %d without an installation id", e.AppID)
	}
	if env.KeyExists == nil || env.ReadKey == nil {
		return app, res, false, fmt.Errorf("the settings name the App %d, but the flow has no way to look for its key secret", e.AppID)
	}
	exists, err := env.KeyExists(ctx)
	if err != nil {
		return app, res, false, fmt.Errorf("look for the App key secret: %w", err)
	}
	var key []byte
	if exists {
		if key, err = env.ReadKey(ctx); err != nil {
			return app, res, false, fmt.Errorf("read the App key secret: %w", err)
		}
	} else if key, err = missingKey(env, fmt.Sprintf("App %d of the settings", e.AppID), appListPages(env.WebURL, *e)); err != nil {
		return app, res, false, err
	}
	app, res, err = verifyApp(ctx, env, e.AppID, e.InstallationID, key)
	return app, res, err == nil, err
}

// verifyApp checks that the App's JWT works (GET /app) and that the installation is one of the App's own.
func verifyApp(ctx context.Context, env githubEnv, appID, installationID int64, key []byte) (appauth.App, GitHubResult, error) {
	app, err := appauth.NewApp(appID, key, env.Now)
	if err != nil {
		return appauth.App{}, GitHubResult{}, err
	}
	slug, err := app.Slug(ctx, env.HTTP, env.APIURL)
	if err != nil {
		return appauth.App{}, GitHubResult{}, fmt.Errorf("verify the App with GET /app: %w", err)
	}
	installations, err := app.Installations(ctx, env.HTTP, env.APIURL)
	if err != nil {
		return appauth.App{}, GitHubResult{}, fmt.Errorf("list the App's installations: %w", err)
	}
	for _, in := range installations {
		if in.ID == installationID {
			ghSay(env.Out, "Using the existing App %s, installation %d on %s.\n", slug, in.ID, in.Account)
			return app, GitHubResult{AppID: appID, Slug: slug, InstallationID: in.ID, Account: in.Account, AccountType: in.AccountType, PrivateKey: key}, nil
		}
	}
	return appauth.App{}, GitHubResult{}, fmt.Errorf("installation %d is not one of the installations of App %s", installationID, slug)
}

// createApp makes the App through the manifest flow and waits for the user to install it. created is the address that
// deletes the App, set as soon as it exists.
func createApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, created string, err error) {
	account, err := env.R.Value("github-account", `GitHub account that will own the App ("personal" or an organization login)`, accountPersonal)
	if err != nil {
		return app, res, "", err
	}
	if !ghLoginPattern.MatchString(account) {
		return app, res, "", fmt.Errorf("github-account %q is not %q or a GitHub login", account, accountPersonal)
	}
	code, appName, err := obtainManifestCode(ctx, env, account)
	if err != nil {
		return app, res, "", err
	}
	conv, err := convertManifest(ctx, env.HTTP, env.APIURL, code, account, appName)
	if conv.Slug != "" {
		created = appPage(env.WebURL, conv.Slug, conv.OwnerLogin, conv.OwnerType)
	}
	if err != nil {
		return app, res, created, err
	}
	if app, err = appauth.NewApp(conv.ID, conv.PrivateKey, env.Now); err != nil {
		return app, res, created, fmt.Errorf("the key GitHub returned for the App %s is not usable: %w", conv.Slug, err)
	}
	ghSay(env.Out, "Created the GitHub App %s.\n", conv.Slug)
	if env.StoreKey != nil { // before the wait: a failure later must not lose the only copy of the key
		if err := env.StoreKey(ctx, conv.PrivateKey); err != nil {
			return app, res, created, fmt.Errorf("store the App key: %w", err)
		}
		ghSay(env.Out, "The App key is in the secret store.\n")
	}
	if env.RecordApp != nil { // after the key: a record without its key would send a re-run to an App it cannot use
		if err := env.RecordApp(ctx, PendingApp{AppID: conv.ID, Slug: conv.Slug, Account: conv.OwnerLogin, AccountType: conv.OwnerType}); err != nil {
			return app, res, created, fmt.Errorf("record the App: %w", err)
		}
	}
	ghSay(env.Out, "Install it: open %s/apps/%s/installations/new, choose %s, and give it access to the repositories to protect.\n", env.WebURL, conv.Slug, conv.OwnerLogin)
	info, err := waitForInstallation(ctx, env, app, conv.OwnerLogin)
	if err != nil {
		return app, res, created, err
	}
	return app, GitHubResult{AppID: conv.ID, Slug: conv.Slug, InstallationID: info.ID, Account: info.Account, AccountType: info.AccountType, PrivateKey: conv.PrivateKey}, created, nil
}

// errPendingKeyMissing means an App an earlier run recorded has no key secret: it cannot be used, and it is not replaced.
var errPendingKeyMissing = errors.New("an earlier run created a GitHub App and recorded it, but its key secret is gone")

// errResuming marks the failure of taking up a recorded App, which may be gone from GitHub: the record and the key remain.
var errResuming = errors.New("the App an earlier run recorded could not be taken up")

// resumeApp takes up the App an earlier run created and recorded: its key is in the secret store, so what is left is the
// installation and the rest of the flow. created is the page that deletes the App.
func resumeApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, created string, err error) {
	p := *env.Pending
	created = appPage(env.WebURL, p.Slug, p.Account, p.AccountType)
	if env.KeyExists == nil || env.ReadKey == nil {
		return app, res, created, fmt.Errorf("the App %s is recorded, but the flow has no way to look for its key secret", p.Slug)
	}
	exists, err := env.KeyExists(ctx)
	if err != nil {
		return app, res, created, fmt.Errorf("look for the App key secret: %w", err)
	}
	var key []byte
	given := false // the key came from a file, not from the secret store
	if exists {
		if key, err = env.ReadKey(ctx); err != nil {
			return app, res, created, fmt.Errorf("read the App key secret: %w", err)
		}
	} else if key, err = missingKey(env, fmt.Sprintf("App %s (id %d)", p.Slug, p.AppID), created); errors.Is(err, errNewApp) {
		return app, res, created, err
	} else if err != nil {
		return app, res, created, fmt.Errorf("%w: %w", errPendingKeyMissing, err)
	} else {
		given = true
	}
	// A key that is given is stored only when the flow ends: whatever fails before, nothing is stored, and the advice for a stored
	// key (retry, remove the secret) does not fit.
	notStored := func(why string, err error) error {
		if !given {
			return err
		}
		return fmt.Errorf("%w: %s (give the key file again with --github-app-key-file, or create a new App with --github %s): %w", errPendingKeyMissing, why, newAppAnswer, err)
	}
	if app, err = appauth.NewApp(p.AppID, key, env.Now); err != nil {
		return app, res, created, notStored("the key file is not the key of the App "+p.Slug, err)
	}
	if _, err = app.Slug(ctx, env.HTTP, env.APIURL); err != nil {
		return app, res, created, notStored("the key file is not the key of the App "+p.Slug, fmt.Errorf("verify the App with GET /app: %w", err))
	}
	if given {
		ghSay(env.Out, "Taking up the GitHub App %s that an earlier run created, with the key you gave.\n", p.Slug)
	} else {
		ghSay(env.Out, "Taking up the GitHub App %s that an earlier run created; its key is in the secret store.\n", p.Slug)
	}
	ghSay(env.Out, "Install it: open %s/apps/%s/installations/new, choose %s, and give it access to the repositories to protect.\n", env.WebURL, p.Slug, p.Account)
	info, err := waitForInstallation(ctx, env, app, p.Account)
	if err != nil {
		return app, res, created, notStored("the key you gave was not stored", err)
	}
	return app, GitHubResult{AppID: p.AppID, Slug: p.Slug, InstallationID: info.ID, Account: info.Account, AccountType: info.AccountType, PrivateKey: key}, created, nil
}

// waitForInstallation polls the App's installations until one is on account, or InstallWait passes.
func waitForInstallation(ctx context.Context, env githubEnv, app appauth.App, account string) (appauth.InstallationInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, env.InstallWait)
	defer cancel()
	ghSay(env.Out, "Waiting for the installation on %s (checking every %s, up to %s)...\n", account, env.PollEvery, env.InstallWait)
	tick := time.NewTicker(env.PollEvery)
	defer tick.Stop()
	for {
		list, err := app.Installations(ctx, env.HTTP, env.APIURL)
		if err == nil {
			for _, in := range list {
				if strings.EqualFold(in.Account, account) {
					return in, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return appauth.InstallationInfo{}, ctx.Err()
			}
			msg := fmt.Sprintf("timed out after %s waiting for the App to be installed on %s", env.InstallWait, account)
			if err != nil {
				msg += fmt.Sprintf(" (the last listing failed: %v)", err)
			}
			return appauth.InstallationInfo{}, errors.New(msg)
		case <-tick.C:
		}
	}
}

// chooseRepositories lists what the installation grants and lets the user choose 1 to MaxRepositories of them.
func chooseRepositories(ctx context.Context, env githubEnv, app appauth.App, res GitHubResult) ([]string, error) {
	all, err := app.InstallationRepositories(ctx, res.InstallationID, env.HTTP, env.APIURL)
	if err != nil {
		return nil, fmt.Errorf("list the repositories of installation %d: %w", res.InstallationID, err)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("the installation covers no repositories: give the App access to at least one repository at %s/apps/%s/installations/%d and run the installer again", env.WebURL, res.Slug, res.InstallationID)
	}
	for _, name := range all {
		if owner, _, _ := strings.Cut(name, "/"); !strings.EqualFold(owner, res.Account) {
			return nil, fmt.Errorf("the installation lists %s, which is not owned by %s: one instance serves one owner", name, res.Account)
		}
	}
	ghSay(env.Out, "The App can reach these repositories:\n")
	for i, name := range all {
		ghSay(env.Out, "  %d) %s\n", i+1, name)
	}
	def := ""
	if len(all) <= MaxRepositories {
		def = strings.Join(all, ",")
	}
	answer, err := env.R.Value("repositories", fmt.Sprintf("Repositories to protect (numbers or owner/name, comma-separated, at most %d)", MaxRepositories), def)
	if err != nil {
		return nil, err
	}
	var chosen []string
	for _, token := range strings.Split(answer, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		var name string
		if n, err := strconv.Atoi(token); err == nil && n >= 1 && n <= len(all) {
			name = all[n-1]
		} else if i := slices.IndexFunc(all, func(a string) bool { return strings.EqualFold(a, token) }); i >= 0 {
			name = all[i]
		} else {
			return nil, fmt.Errorf("%q is not one of the repositories the installation covers", token)
		}
		if !slices.Contains(chosen, name) {
			chosen = append(chosen, name)
		}
	}
	if len(chosen) < 1 || len(chosen) > MaxRepositories {
		return nil, fmt.Errorf("%d repositories chosen: an instance serves between 1 and %d", len(chosen), MaxRepositories)
	}
	return chosen, nil
}

func repositoryNames(repos []string) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		_, names[i], _ = strings.Cut(r, "/")
	}
	return names
}

// ghAccess builds the REST clients of installation tokens narrowed to a permission map, with one budget for all.
type ghAccess struct {
	app    appauth.App
	id     int64
	names  []string
	http   *http.Client
	api    string
	budget *rest.Budget
}

func (a ghAccess) client(permissions map[string]string) (*rest.Client, error) {
	installation, err := a.app.Installation(a.id, a.names, permissions, a.http, a.api)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub installation: %w", err)
	}
	client, err := rest.NewClient(func(ctx context.Context) (value, identity string, err error) {
		token, err := installation.Token(ctx)
		if err != nil {
			return "", "", err
		}
		return token.Reveal(), token.Identity(), nil
	}, a.http, a.api, a.budget)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub client: %w", err)
	}
	return client, nil
}

// The two token maps of the installer, both inside the App's five permissions: reading check runs, and reading protection
// (the same map as the service's protection check).
var (
	checksReadPermissions = map[string]string{"metadata": "read", "checks": "read"}
	protectionPermissions = map[string]string{"metadata": "read", "administration": "read"}
)

// repoInfo is what the flow needs to know of a chosen repository.
type repoInfo struct {
	name, branch string
	private      bool
}

func readRepositories(ctx context.Context, a ghAccess, repos []string) ([]repoInfo, error) {
	client, err := a.client(checksReadPermissions)
	if err != nil {
		return nil, err
	}
	reader, err := github.NewReader(client)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub reader: %w", err)
	}
	infos := make([]repoInfo, 0, len(repos))
	for _, name := range repos {
		repo, err := reader.Repository(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("read the repository %s: %w", name, err)
		}
		infos = append(infos, repoInfo{name: name, branch: repo.DefaultBranch, private: repo.Private})
	}
	return infos, nil
}

// checkRunNames lists the check runs on the head of the default branch, without duplicates and without the contract check.
// shortcut: the first 100 runs only, and legacy commit statuses do not show as check runs: the user types such a name.
func checkRunNames(ctx context.Context, client *rest.Client, r repoInfo) ([]string, error) {
	if r.branch == "." || r.branch == ".." {
		return nil, fmt.Errorf("branch %q cannot be named in a URL path", r.branch)
	}
	resp, err := client.Get(ctx, "/repos/"+r.name+"/commits/"+url.PathEscape(r.branch)+"/check-runs?per_page=100")
	if err != nil {
		return nil, err
	}
	var out struct {
		CheckRuns []struct {
			Name string `json:"name"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, errors.New("the check runs answer is not the expected JSON")
	}
	var names []string
	for _, run := range out.CheckRuns {
		if run.Name != "" && run.Name != protection.ContractCheck && !slices.Contains(names, run.Name) {
			names = append(names, run.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// chooseTestChecks offers the check runs of each repository's default branch, or asks for the name CI will publish.
func chooseTestChecks(ctx context.Context, env githubEnv, a ghAccess, repos []repoInfo) (map[string]string, error) {
	client, err := a.client(checksReadPermissions)
	if err != nil {
		return nil, err
	}
	value, _ := env.R.given("test-check")
	listed, isList, err := parseTestCheckList(value)
	if err != nil {
		return nil, err
	}
	for repo := range listed {
		if !slices.ContainsFunc(repos, func(r repoInfo) bool { return strings.EqualFold(r.name, repo) }) {
			return nil, fmt.Errorf("test-check names %s, which is not one of the chosen repositories", repo)
		}
	}
	checks := make(map[string]string, len(repos))
	for _, r := range repos {
		key := "test-check." + r.name
		_, perRepo := env.R.given(key)
		var answer string
		switch fromList := lookupFold(listed, r.name); {
		case !perRepo && fromList != "":
			answer = fromList
		case !perRepo && !isList && len(repos) == 1:
			key = "test-check"
			fallthrough
		default:
			if answer, err = askTestCheck(ctx, env, client, r, key); err != nil {
				return nil, err
			}
		}
		switch answer {
		case "":
			return nil, fmt.Errorf("the test check of %s is empty", r.name)
		case protection.ContractCheck:
			return nil, fmt.Errorf("%s is SuiteWard's own check, not the project's test check", protection.ContractCheck)
		}
		checks[r.name] = answer
	}
	return checks, nil
}

// parseTestCheckList reads OWNER/NAME=CHECK entries, one per line: the shape of setup's repeated --test-check, joined by
// the caller. ok is false when the value is a plain check name. A repository named twice, in any letter case, is an error.
func parseTestCheckList(value string) (checks map[string]string, ok bool, err error) {
	checks = map[string]string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		repo, check, found := strings.Cut(line, "=")
		if check = strings.TrimSpace(check); !found || !repositoryName.MatchString(repo) || check == "" {
			return nil, false, nil
		}
		lower := strings.ToLower(repo)
		if seen[lower] {
			return nil, false, fmt.Errorf("test-check lists %s twice (repository names are not case-sensitive)", repo)
		}
		seen[lower] = true
		checks[repo] = check
	}
	return checks, len(checks) > 0, nil
}

// lookupFold finds key in m ignoring letter case; parseTestCheckList guarantees at most one match.
func lookupFold(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// askTestCheck offers the check runs of the repository's default branch, or asks for the name CI will publish.
func askTestCheck(ctx context.Context, env githubEnv, client *rest.Client, r repoInfo, key string) (string, error) {
	names, err := checkRunNames(ctx, client, r)
	if err != nil {
		ghSay(env.Out, "Could not read the check runs of %s (%v); you will type the name.\n", r.name, err)
	}
	def, label := "", fmt.Sprintf("Name of the check your CI publishes for %s", r.name)
	if len(names) > 0 {
		ghSay(env.Out, "Check runs on the %s branch of %s:\n", r.branch, r.name)
		for i, n := range names {
			ghSay(env.Out, "  %d) %s\n", i+1, n)
		}
		label = fmt.Sprintf("Required test check for %s (number or name)", r.name)
		if len(names) == 1 {
			def = names[0]
		}
	} else {
		ghSay(env.Out, "%s has no check runs yet. Type the name your CI will publish; until a check with that name exists, /status reports missing-test-check.\n", r.name)
	}
	answer, err := env.R.Value(key, label, def)
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(names) {
		answer = names[n-1]
	}
	return answer, nil
}

// importRulesets writes one ruleset file per repository, tells the user how to import it, waits for the import and re-reads
// the protection of each repository.
func importRulesets(ctx context.Context, env githubEnv, a ghAccess, res GitHubResult, repos []repoInfo) (map[string]protection.Result, error) {
	for _, r := range repos {
		data, err := rulesetJSON(res.AppID, res.TestChecks[r.name])
		if err != nil {
			return nil, fmt.Errorf("ruleset of %s: %w", r.name, err)
		}
		path, err := writeRuleset(env.SettingsDir, r.name, data)
		if err != nil {
			return nil, fmt.Errorf("write the ruleset of %s: %w", r.name, err)
		}
		ghSay(env.Out, "\nRuleset for %s: %s\n", r.name, path)
		if r.private {
			ghSay(env.Out, "Warning: %s is a private repository. On GitHub Free, rules are not enforced on private repositories, so SuiteWard cannot protect it until the repository is a public repository or the account is on a paid plan (Team). The install continues, and /status will report enforcement-unavailable.\n", r.name)
		}
		ghSay(env.Out, "Import it: open %s/%s/settings/rules, choose New ruleset, then Import a ruleset, select that file and create the ruleset.\n", env.WebURL, r.name)
	}
	if !env.SkipRulesetWait {
		if _, err := env.R.Value("ruleset-imported", "\nPress Enter when the rulesets are imported", "yes"); err != nil {
			return nil, err
		}
	}
	client, err := a.client(protectionPermissions)
	if err != nil {
		return nil, err
	}
	reader, err := github.NewReader(client)
	if err != nil {
		return nil, fmt.Errorf("compose the GitHub reader: %w", err)
	}
	results := make(map[string]protection.Result, len(repos))
	ghSay(env.Out, "\nProtection as GitHub reports it now:\n")
	for _, r := range repos {
		result := protection.Unknown()
		read, err := reader.Protection(ctx, r.name, r.branch)
		if err != nil {
			ghSay(env.Out, "  %s: could not read the protection (%v)\n", r.name, err)
		} else if facts, err := read.Facts(r.private); err != nil {
			ghSay(env.Out, "  %s: the protection answer is not usable (%v)\n", r.name, err)
		} else {
			result = protection.Evaluate(facts, res.AppID, res.TestChecks[r.name])
		}
		results[r.name] = result
		ghSay(env.Out, "  %s: %s\n", r.name, result.Kind)
		if len(result.Findings) > 0 {
			codes := make([]string, len(result.Findings))
			for i, c := range result.Findings {
				codes[i] = string(c)
			}
			ghSay(env.Out, "    findings: %s\n", strings.Join(codes, ", "))
		}
		for _, line := range result.Instructions {
			ghSay(env.Out, "    - %s\n", line)
		}
	}
	ghSay(env.Out, "Run the checks again at any time with suiteward doctor.\n")
	return results, nil
}

// runSetup runs `suiteward setup` in the serving container with the real terminal passed through. Setup prints the TOTP
// enrollment URI and secret there, so nothing is captured or logged, and the user is told to stop session recording first.
func runSetup(ctx context.Context, rt Runtime, owner string, checks map[string]string, out io.Writer) error {
	if !ghLoginPattern.MatchString(owner) {
		return fmt.Errorf("owner %q is not a GitHub login", owner)
	}
	if len(checks) == 0 {
		return errors.New("setup needs the test check of at least one repository")
	}
	repos := make([]string, 0, len(checks))
	for repo, check := range checks {
		if !repositoryName.MatchString(repo) || strings.TrimSpace(check) == "" {
			return fmt.Errorf("the test check %q of %q is not a repository (owner/name) with a check name", check, repo)
		}
		if strings.TrimSpace(check) == protection.ContractCheck {
			return fmt.Errorf("the test check of %s is %s, SuiteWard's own check, not the project's test check", repo, protection.ContractCheck)
		}
		if len(check) > appowner.MaxTestCheckBytes { // the limit setup enforces, so setup does not refuse after the container is entered
			return fmt.Errorf("the test check of %s is longer than %d bytes", repo, appowner.MaxTestCheckBytes)
		}
		repos = append(repos, repo)
	}
	// Setup prints the TOTP enrollment URI and secret on its terminal: with no real terminal here, that output would go
	// to a pipe or a file that a coding agent could read, so the installer does not run it.
	if !setupTerminal() {
		return errors.New("setup needs a real terminal on stdin and stdout: run the installer from your own terminal, not through a pipe, a script or a coding agent")
	}
	sort.Strings(repos)
	argv := []string{"/suiteward", "setup", "--owner", owner}
	for _, repo := range repos {
		argv = append(argv, "--test-check", repo+"="+checks[repo])
	}
	ghSay(out, "\nSetup enrolls you as the owner and prints a TOTP enrollment URI and secret, only on this terminal. Stop any session recording or terminal logging now, and make sure no coding agent can read this terminal. The installer does not capture or log setup's output.\n")
	if err := rt.ExecTTY(ctx, ContainerServe, argv); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}
	return nil
}

// setupTerminal tells whether setup can have a real terminal: stdin and stdout are both terminals. Tests replace it.
var setupTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// wantsNewApp says whether --github new-app asks for a new App although the settings or an earlier run name one.
func wantsNewApp(r *Resolver) bool {
	v, _ := r.given("github")
	return strings.EqualFold(strings.TrimSpace(v), newAppAnswer)
}
