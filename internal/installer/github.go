package installer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
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
// Answer keys (flag names and answers-file keys): github-account (personal, or an organization login that GitHub is asked about before the browser opens, unless manifest-code is given), manifest-code (the answers file only: it is a credential for an hour),
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
	env.R.explain([]string{"owner"}, "Instance owner", ownerPage)
	if res.OwnerLogin, err = env.R.Value("owner", "GitHub username of the owner, the only account that can approve changes", defaultOwner); err != nil {
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

// ownerPage is the explanation of the owner question, which the install and enroll draw on a full screen that is going to ask it.
var ownerPage = ui.Para("Your GitHub username, as in github.com/<username>. Only this account can approve changes, with /suiteward commands. It cannot be an organization.")

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
		return fmt.Sprintf("the secret %s already holds the key of the App %s that an earlier run created, so nothing was changed. To use that App, run again without --github new-app. To replace it instead, remove the secret and the record, and delete the App at %s",
			SecretGitHubAppKey, p.Slug, appPage(env.WebURL, p.Slug, p.Account, p.AccountType))
	case env.Existing != nil && env.Existing.AppID != 0:
		return fmt.Sprintf("the secret %s holds the key of the App %d of the settings, and a new App's key cannot be stored over it, so nothing was changed. Remove the secret, then run again: the App %d then stops working on this instance and stays on GitHub (delete it at %s)",
			SecretGitHubAppKey, env.Existing.AppID, env.Existing.AppID, appListPages(env.WebURL, *env.Existing))
	}
	return fmt.Sprintf("the secret %s exists, and a new App's key cannot be stored over it, so nothing was changed: remove the secret, then run again", SecretGitHubAppKey)
}

// replacedNote says that the App the settings named, and the App an earlier run recorded, stay on GitHub, and where each is deleted.
func replacedNote(env githubEnv) string {
	var note string
	if env.Existing != nil && env.Existing.AppID != 0 {
		note = fmt.Sprintf("The App %d that the settings named stays on GitHub. You can delete it at %s.\n", env.Existing.AppID, appListPages(env.WebURL, *env.Existing))
	}
	if p := env.Pending; recordWins(env.Existing, p) {
		note += fmt.Sprintf("The App %s that an earlier run recorded stays on GitHub. You can delete it at %s; the record is replaced.\n", p.Slug, appPage(env.WebURL, p.Slug, p.Account, p.AccountType))
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
		return nil, fmt.Errorf("the %s has no key in the secret store (%s), so nothing was changed. Pass --github-app-key-file <file> with the App's private key (create one on the App's settings page if you have none), or pass --github %s to create a new App (the old one stays on GitHub: delete it at %s)",
			who, SecretGitHubAppKey, newAppAnswer, deleteAt)
	}
	if scr := env.R.screen; scr.FullScreen() {
		scr.Page("The App has no key",
			ui.Para(fmt.Sprintf("The %s has no key in the secret store (%s), so SuiteWard cannot use it.", who, SecretGitHubAppKey)),
			ui.Para("Give the App's private key file, or create a new App: the old one stays on GitHub, and you can delete it at "+deleteAt+"."))
		picked, err := scr.Select("What do you want to do?", []ui.Choice{
			{Name: "key file", Desc: "give the App's private key file"},
			{Name: "new App", Desc: "create a new App; the old one stays on GitHub"},
		}, 0)
		switch {
		case err != nil:
			return nil, fmt.Errorf("read the choice: %w", err)
		case picked == 0:
			return readAppKey(env.R)
		}
		return nil, errNewApp
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
	return fmt.Errorf("%w (an App was created by this run or an earlier one: if you cannot go on, delete it at %s)", err, deleteURL)
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
	PasteAfter      time.Duration // how long the callback may take before the paste is offered, 2 minutes
	InstallWait     time.Duration // the installation, 10 minutes
	PollEvery       time.Duration // 5 seconds

	// Command is how a message names this program (Deps.command()); empty reads suiteward.
	Command string

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
	if e.PasteAfter == 0 {
		e.PasteAfter = 2 * time.Minute
	}
	if e.InstallWait == 0 {
		e.InstallWait = 10 * time.Minute
	}
	if e.PollEvery == 0 {
		e.PollEvery = 5 * time.Second
	}
	e.Command = cmp.Or(e.Command, selfName)
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

// asks says whether Value of key would put a question to the person at the terminal: no flag or answer gives it, and there is one.
func (r *Resolver) asks(key string) bool {
	_, given := r.given(key)
	return !given && r.term.IsTerminal
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
			env.R.screen.Check("Using the existing App %s, installation %d on %s.\n", slug, in.ID, in.Account)
			return app, GitHubResult{AppID: appID, Slug: slug, InstallationID: in.ID, Account: in.Account, AccountType: in.AccountType, PrivateKey: key}, nil
		}
	}
	return appauth.App{}, GitHubResult{}, fmt.Errorf("installation %d is not one of the installations of App %s", installationID, slug)
}

// organizationAnswer is the answer of the account question that asks for an organization's login next.
// shortcut: an organization literally named "organization" cannot be chosen, as none named "personal" can; give such a login in a flag if that ever matters.
const organizationAnswer = "organization"

// accountRefused is why an account cannot own the App: a person who is asked can answer again, a flag or an answers file cannot.
type accountRefused string

func (e accountRefused) Error() string { return string(e) }

// chooseAccount finds the account that will own the App: "personal", or the login of an organization that exists. On a terminal a
// refusal is explained and the question asked again, from its start; an answer that came from a flag or the answers file is refused for
// good, because it would come back unchanged. The organization is looked up before the browser opens, so that a typo or a word that
// is no organization does not become an App under the wrong account. A manifest code that is given exists only once GitHub has made
// the App, so it is not looked up: the conversion's own check names the App it refuses.
func chooseAccount(ctx context.Context, env githubEnv) (string, error) {
	_, haveCode := env.R.given("manifest-code")
	if env.R.screen.FullScreen() && env.R.asks("github-account") {
		env.R.screen.Page("GitHub account for the App", ui.Para("SuiteWard creates its GitHub App under one account: your personal account, or an organization where you can create GitHub Apps. It must be the account that owns the repositories to protect."))
	}
	for {
		account, typed, err := askAccount(env)
		if err == nil {
			err = checkAppOwner(ctx, env, account, !haveCode)
		}
		var refused accountRefused
		var failed lookupFailed
		switch {
		case err == nil:
			return account, nil
		case errors.As(err, &failed): // nothing is known of the account: the conversion checks its owner after the App is made
			env.R.screen.Warn("Could not check %s on GitHub (%s). This is not a problem: GitHub checks the account again after the App is created.\n", account, failed)
			return account, nil
		case !errors.As(err, &refused) || !typed:
			return "", err
		}
		ghSay(env.Out, "%s\n", refused)
	}
}

// askAccount asks for the account: personal or an organization on a full screen, a typed "personal" or login elsewhere (the question
// of the flag and the answers-file key github-account, which also takes a login). Choosing organization asks for its login. typed says
// that a terminal gave the answer.
func askAccount(env githubEnv) (account string, typed bool, err error) {
	const key, label = "github-account", "GitHub account that will own the App"
	typed = env.R.asks(key)
	if env.R.screen.FullScreen() && typed {
		account, err = env.R.Select(key, label, []ui.Choice{
			{Name: accountPersonal, Desc: "your own GitHub account"},
			{Name: organizationAnswer, Desc: "an organization; asks its login"},
		}, accountPersonal)
	} else {
		account, err = env.R.Value(key, label+` ("personal" or an organization login)`, accountPersonal)
	}
	switch {
	case err != nil:
		return "", false, err
	case strings.EqualFold(account, accountPersonal):
		return accountPersonal, typed, nil
	case !strings.EqualFold(account, organizationAnswer):
		return account, typed, nil
	case !env.R.term.IsTerminal:
		return "", false, fmt.Errorf("%s is %q: name the organization with its login (--github-account <login>)", key, account)
	}
	account, err = env.R.screen.Ask("Organization login (as in github.com/<org>)", "")
	return account, true, err
}

// checkAppOwner refuses what cannot own the App: a word that is no login, and, when lookup is set, an account that GitHub does not know
// or that is not an organization.
func checkAppOwner(ctx context.Context, env githubEnv, account string, lookup bool) error {
	switch {
	case account == accountPersonal:
		return nil
	case !ghLoginPattern.MatchString(account):
		return accountRefused(fmt.Sprintf("github-account %q is not %q or a GitHub login", account, accountPersonal))
	case !lookup:
		return nil
	}
	return lookupOrganization(ctx, env, account)
}

// lookupFailed is why the look-up of an organization says nothing about it: a rate limit, an error of GitHub, the network, an answer
// that is no account. It stops nothing: the conversion checks the owner of the App once it exists.
type lookupFailed string

func (e lookupFailed) Error() string { return string(e) }

// lookupOrganization asks GitHub, with no authentication, whether login is an organization. Only 404 and an account that is no
// organization refuse it; whatever else cannot be decided is a lookupFailed, which names the status and no text of the answer.
// shortcut: an unauthenticated call is limited to 60 an hour per address, and whether the user may create Apps in the organization is
// not asked: the conversion refuses an App that another account owns. Upgrade when the flow holds a token before this point.
func lookupOrganization(ctx context.Context, env githubEnv, login string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(env.APIURL, "/")+"/users/"+url.PathEscape(login), nil)
	if err != nil {
		return fmt.Errorf("build the request that looks up the organization %s: %w", login, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	failed := func(why string) error { // Ctrl-C is not a failure of GitHub, whichever way the look-up meets it
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return lookupFailed(why)
	}
	resp, err := env.HTTP.Do(req)
	if err != nil {
		var urlErr *url.Error // its text names the URL
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return failed(err.Error())
	}
	defer func() { _ = resp.Body.Close() }() // the answer is read below; a close error cannot change it
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return accountRefused("GitHub has no account named " + login)
	default:
		return failed(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	var out struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxConversionAnswer)).Decode(&out); err != nil {
		return failed("an answer that is not an account")
	}
	if out.Type != "Organization" {
		return accountRefused(login + " is a personal account, not an organization (answer personal to create the App under your own account)")
	}
	return nil
}

// createApp makes the App through the manifest flow and waits for the user to install it. created is the address that
// deletes the App, set as soon as it exists.
func createApp(ctx context.Context, env githubEnv) (app appauth.App, res GitHubResult, created string, err error) {
	account, err := chooseAccount(ctx, env)
	if err != nil {
		return app, res, "", err
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
	env.R.screen.Check("Created the GitHub App %s.\n", conv.Slug)
	if env.StoreKey != nil { // before the wait: a failure later must not lose the only copy of the key
		if err := env.StoreKey(ctx, conv.PrivateKey); err != nil {
			return app, res, created, fmt.Errorf("store the App key: %w", err)
		}
		env.R.screen.Check("The App key is in the secret store.\n")
	}
	if env.RecordApp != nil { // after the key: a record without its key would send a re-run to an App it cannot use
		if err := env.RecordApp(ctx, PendingApp{AppID: conv.ID, Slug: conv.Slug, Account: conv.OwnerLogin, AccountType: conv.OwnerType}); err != nil {
			return app, res, created, fmt.Errorf("record the App: %w", err)
		}
	}
	info, err := waitForInstallation(ctx, env, app, conv.Slug, conv.OwnerLogin)
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
		env.R.screen.Check("Taking up the GitHub App %s that an earlier run created, with the key you gave.\n", p.Slug)
	} else {
		env.R.screen.Check("Taking up the GitHub App %s that an earlier run created; its key is in the secret store.\n", p.Slug)
	}
	info, err := waitForInstallation(ctx, env, app, p.Slug, p.Account)
	if err != nil {
		return app, res, created, notStored("the key you gave was not stored", err)
	}
	return app, GitHubResult{AppID: p.AppID, Slug: p.Slug, InstallationID: info.ID, Account: info.Account, AccountType: info.AccountType, PrivateKey: key}, created, nil
}

// waitForInstallation tells the user to install the App on account, and polls the App's installations until one is on account, or
// InstallWait passes.
func waitForInstallation(ctx context.Context, env githubEnv, app appauth.App, slug, account string) (info appauth.InstallationInfo, err error) {
	ctx, cancel := context.WithTimeout(ctx, env.InstallWait)
	defer cancel()
	address := fmt.Sprintf("%s/apps/%s/installations/new", env.WebURL, slug)
	scr := env.R.screen
	if scr.FullScreen() {
		scr.Page("Install the GitHub App",
			ui.Para(fmt.Sprintf("The App exists. Install it on %s and give it access to the repositories to protect (at most %d). The installer goes on by itself when it sees the installation.", account, MaxRepositories)),
			ui.Fields(ui.KV{Key: "Address", Value: address}, ui.KV{Key: "Account", Value: account}))
	} else {
		ghSay(env.Out, "Install it: open %s, choose %s, and give it access to the repositories to protect.\n", address, account)
		ghSay(env.Out, "Waiting for the installation on %s (checking every %s, up to %s)...\n", account, env.PollEvery, env.InstallWait)
	}
	spinning(scr, "Waiting for the installation on "+account, func() { info, err = pollInstallation(ctx, env, app, account) })
	return info, err
}

// pollInstallation looks for the installation every PollEvery until ctx is done.
func pollInstallation(ctx context.Context, env githubEnv, app appauth.App, account string) (appauth.InstallationInfo, error) {
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
	scr := env.R.screen
	def, defaults := "", []int(nil)
	if len(all) <= MaxRepositories {
		def = strings.Join(all, ",")
		for i := range all {
			defaults = append(defaults, i)
		}
	}
	hint := fmt.Sprintf("numbers or owner/name, comma-separated, at most %d", MaxRepositories)
	if scr.FullScreen() && env.R.asks("repositories") {
		scr.Page("Repositories to protect", ui.Para(fmt.Sprintf("SuiteWard protects the repositories you select, at most %d. Each gets a GitHub rule (a ruleset) that requires two checks before a change is merged: SuiteWard / Contract and your test check.", MaxRepositories)))
		choices := make([]ui.Choice, len(all))
		for i, name := range all {
			choices[i] = ui.Choice{Name: name}
		}
		return env.R.MultiSelect("repositories", ui.Multi{Label: "Repositories to protect", Choices: choices, Defaults: defaults, Min: 1, Max: MaxRepositories, Hint: hint})
	}
	if !scr.FullScreen() { // a full screen that does not ask has no use for the list
		ghSay(env.Out, "The App can reach these repositories:\n")
		for i, name := range all {
			ghSay(env.Out, "  %d) %s\n", i+1, name)
		}
	}
	answer, err := env.R.Value("repositories", "Repositories to protect ("+hint+")", def)
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

// ownCheck is whether a check name is one of the two that SuiteWard publishes: neither is the project's test check.
func ownCheck(name string) bool {
	return name == protection.ContractCheck || name == github.ReceiptCheckName
}

// checkRunNames lists the check runs on the head of the default branch, without duplicates and without the two checks SuiteWard publishes.
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
		if run.Name != "" && run.Name != protection.ContractCheck && run.Name != github.ReceiptCheckName && !slices.Contains(names, run.Name) {
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
		case protection.ContractCheck, github.ReceiptCheckName:
			return nil, fmt.Errorf("%s is SuiteWard's own check, not the project's test check", answer)
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

// askTestCheck offers the check runs of the repository's default branch, or asks for the name CI will publish. A full screen that asks
// and has check runs to offer picks one with the arrow keys (pickTestCheck); everything else is the typed question.
func askTestCheck(ctx context.Context, env githubEnv, client *rest.Client, r repoInfo, key string) (string, error) {
	asking := env.R.screen.FullScreen() && env.R.asks(key)
	if asking { // one repository to a page, before the list is read: the list goes under it
		env.R.screen.Page("Required test check for "+r.name,
			ui.Para("SuiteWard does not replace your CI's tests: a pull request must pass both "+protection.ContractCheck+" and your CI's test check. Pick the test check from the checks seen on the default branch."))
	}
	names, err := checkRunNames(ctx, client, r)
	if err != nil {
		ghSay(env.Out, "Could not read the checks of %s (%v), so you type the name.\n", r.name, err)
	}
	if asking && len(names) > 0 {
		return pickTestCheck(env.R, r, names, key)
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
		ghSay(env.Out, "%s has no checks yet. Type the name your CI will publish. Until a check with that name exists, the status shows missing-test-check, which is expected.\n", r.name)
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

// testCheckDefault is the index of the check run that Enter takes: the only one there is, or the only one with "test" in its name (in any
// case). With several runs and none or more than one of those names, it is -1: no run is a better guess than another, and the person picks.
func testCheckDefault(names []string) int {
	if len(names) == 1 {
		return 0
	}
	found := -1
	for i, name := range names {
		if strings.Contains(strings.ToLower(name), "test") {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

// pickTestCheck is the check runs seen, picked with the arrow keys with the one that testCheckDefault names highlighted, and one more
// choice at the end: another name, which is then typed.
func pickTestCheck(r *Resolver, repo repoInfo, names []string, key string) (string, error) {
	choices := make([]ui.Choice, 0, len(names)+1)
	for _, name := range names {
		choices = append(choices, ui.Choice{Name: name})
	}
	choices = append(choices, ui.Choice{Name: "another name…"})
	picked, err := r.screen.Select("Required test check for "+repo.name, choices, testCheckDefault(names))
	if err != nil {
		return "", err
	}
	if picked < len(names) {
		return names[picked], nil
	}
	typed, err := r.Value(key, "Name of the check your CI publishes for "+repo.name, "")
	return strings.TrimSpace(typed), err
}

// importRulesets writes one ruleset file per repository, tells the user how to import it, waits for the import and re-reads
// the protection of each repository. A full screen that asks pauses the user at a page of one compact row per repository
// (pauseForImport); everywhere else the question is the one it was, whose answer, a typed no included, only makes the protection be
// read: what is missing is reported after it all the same.
func importRulesets(ctx context.Context, env githubEnv, a ghAccess, res GitHubResult, repos []repoInfo) (map[string]protection.Result, error) {
	scr := env.R.screen
	pausing := !env.SkipRulesetWait && scr.FullScreen() && env.R.asks("ruleset-imported")
	files := make(map[string]string, len(repos)) // where each ruleset is
	if pausing {
		for _, r := range repos {
			path, err := writeRulesetFile(env.SettingsDir, res, r)
			if err != nil {
				return nil, err
			}
			files[r.name] = path
		}
		// One page, before the warnings below, which a page that came after them would take off the screen. What the user must keep is
		// kept when the pause is over (keepImports), and only for what is left to do.
		importPage(scr, "Import each file on GitHub: this turns the protection on.", env.WebURL, repos, files, nil)
		for _, r := range repos {
			warnPrivate(scr, r)
		}
	} else {
		if scr.FullScreen() { // before the lines below: a page that comes after them takes them off the screen
			scr.Page("Import the rulesets", ui.Para("Import the file of each repository on GitHub, as shown below. This turns the protection on. If an older SuiteWard ruleset (from an earlier App) is already there, delete it first: both would apply."))
		}
		for _, r := range repos {
			path, err := writeRulesetFile(env.SettingsDir, res, r)
			if err != nil {
				return nil, err
			}
			files[r.name] = path
			keepImport(scr, r, path)
			warnPrivate(scr, r)
			keepSteps(scr, env.WebURL, r)
		}
	}
	if !env.SkipRulesetWait && !pausing {
		if env.R.asks("ruleset-imported") {
			ghSay(env.Out, "\n") // the blank line is not part of the label: an interrupted question names its label in one line
		}
		if _, err := env.R.Value("ruleset-imported", "Press Enter when the rulesets are imported", "yes"); err != nil {
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
	if pausing {
		return pauseForImport(ctx, scr, env.Command, env.WebURL, reader, res, repos, files)
	}
	results, lines := readProtections(ctx, reader, res, repos)
	dropImported(scr, repos, files, results)
	keepProtection(scr, env.Command, lines)
	return results, nil
}

// writeRulesetFile writes the ruleset of r in dir, and returns the file.
func writeRulesetFile(dir string, res GitHubResult, r repoInfo) (string, error) {
	data, err := rulesetJSON(res.AppID, res.TestChecks[r.name])
	if err != nil {
		return "", fmt.Errorf("ruleset of %s: %w", r.name, err)
	}
	path, err := writeRuleset(dir, r.name, data)
	if err != nil {
		return "", fmt.Errorf("write the ruleset of %s: %w", r.name, err)
	}
	return path, nil
}

// keepImport prints where the ruleset of r is, as a line that stays: the file and how to import it are needed after the page of this
// step is gone.
func keepImport(scr *ui.Screen, r repoInfo, path string) {
	scr.Keep("\nRuleset for %s: %s\n", r.name, path)
}

// keepSteps prints how to import the ruleset of r, as a line that stays.
func keepSteps(scr *ui.Screen, webURL string, r repoInfo) {
	scr.Keep("Import it: open %s, choose New ruleset, then Import a ruleset, select that file and create the ruleset.\n", rulesPageURL(webURL, r))
}

// warnPrivate warns that a private repository cannot be protected on GitHub Free.
func warnPrivate(scr *ui.Screen, r repoInfo) {
	if r.private {
		scr.Warn("%s is a private repository. On GitHub Free, rules are not enforced on private repositories, so SuiteWard cannot protect it until the repository is public or the account is on a paid plan (Team). The install goes on, and the status shows enforcement-unavailable, which is expected.\n", r.name)
	}
}

// dropImported deletes the ruleset file of each repository whose protection reads as enforced: the import worked, and the file is not
// needed any more (owner decision 20). A protection that still needs the import, or whose enforcement is unavailable, keeps its file.
func dropImported(scr *ui.Screen, repos []repoInfo, files map[string]string, results map[string]protection.Result) {
	for _, r := range repos {
		if results[r.name].Kind != protection.KindEnforced || files[r.name] == "" {
			continue
		}
		if err := os.Remove(files[r.name]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			scr.Warn("Could not delete the ruleset file %s (%v). You do not need it any more, so you can delete it yourself.\n", files[r.name], err)
			continue
		}
		scr.Keep("The ruleset file of %s was deleted: the import worked.\n", r.name)
	}
}

// keepImports first deletes the files that are done (dropImported), then prints, as lines that stay, the file and the rules page of each
// repository that is not protected: what is left to do once the page of the pause is gone.
func keepImports(scr *ui.Screen, webURL string, repos []repoInfo, files map[string]string, results map[string]protection.Result) {
	dropImported(scr, repos, files, results)
	for _, r := range repos {
		if results[r.name].Kind != protection.KindEnforced {
			keepImport(scr, r, files[r.name])
			keepSteps(scr, webURL, r)
		}
	}
}

// importPage draws the page of the import: for each repository in repos its name and the file of its ruleset, the link of its rules page
// and what is missing, when missing says it; then how to import. The folder of the files comes first, and the explanation last: a page
// that is too long for the screen loses the end of it, and the user imports from the folder and the rows.
func importPage(scr *ui.Screen, lead, webURL string, repos []repoInfo, files map[string]string, missing map[string]string) {
	rows := make([]ui.KV, 0, 3*len(repos))
	dir := ""
	for _, r := range repos {
		_, name, _ := strings.Cut(r.name, "/") // one instance serves one owner: the short name is enough to tell the rows apart
		rows = append(rows, ui.KV{Key: name, Value: filepath.Base(files[r.name])}, ui.KV{Value: rulesPageURL(webURL, r)})
		if m := missing[r.name]; m != "" {
			rows = append(rows, ui.KV{Value: m})
		}
		dir = filepath.Dir(files[r.name])
	}
	scr.Page("Import the rulesets", ui.Para(lead+" The files are in "+dir+":"), ui.Fields(rows...),
		ui.Para("Open the link of each repository, choose New ruleset, then Import a ruleset, select its file and create the ruleset. If an older SuiteWard ruleset (from an earlier App) is already there, delete it first: both would apply."))
}

// rulesPageURL is the page of the rules of r on GitHub, where its ruleset is imported.
func rulesPageURL(webURL string, r repoInfo) string { return webURL + "/" + r.name + "/settings/rules" }

// pauseForImport holds the user at the page while the rulesets are imported on GitHub, and reads the protection back when they say so. A
// repository that is not protected yet, or whose protection cannot be read, is shown with what is missing and the same two choices are
// offered again; enforcement that is unavailable ends it, as no import can change that. Skipping goes on with a warning: the
// results are what the last check found, or unknown when there was none, and what the last check found stays with it. What is left to
// do, the file and the rules page of each repository that is not protected, is kept when the pause is over.
func pauseForImport(ctx context.Context, scr *ui.Screen, cmd, webURL string, reader *github.Reader, res GitHubResult, repos []repoInfo, files map[string]string) (map[string]protection.Result, error) {
	choices := []ui.Choice{
		{Name: "I imported it, check now", Desc: "reads the protection from GitHub"},
		{Name: "Skip, import later", Desc: "go on; nothing is protected until you import"},
	}
	var results map[string]protection.Result
	var lines []string
	for {
		picked, err := scr.Select("Ruleset import", choices, 0)
		if err != nil { // Ctrl-C, or the input ended: the page goes with the screen, and the files with it unless they are kept
			keepImports(scr, webURL, repos, files, unchecked(repos, results))
			return nil, fmt.Errorf("read the choice: %w", err)
		}
		if picked == 1 {
			break
		}
		if results, lines = readProtections(ctx, reader, res, repos); !awaitingImport(results) {
			keepImports(scr, webURL, repos, files, results)
			keepProtection(scr, cmd, lines)
			return results, nil
		}
		missing := make(map[string]string, len(repos))
		var open []repoInfo
		for _, r := range repos {
			if mayBeFixedByImport(results[r.name]) {
				missing[r.name] = unfinishedText(results[r.name])
				open = append(open, r)
			}
		}
		importPage(scr, "Not enforced yet.", webURL, open, files, missing)
	}
	results = unchecked(repos, results)
	keepImports(scr, webURL, repos, files, results)
	if lines != nil { // what the last check found stays with it
		keepProtection(scr, cmd, lines)
	}
	var open []string
	for _, r := range repos {
		if mayBeFixedByImport(results[r.name]) {
			open = append(open, r.name)
		}
	}
	scr.Warn("The rulesets of %s are not imported or not checked yet, so these repositories are not protected. Import the files named above on GitHub, then run `%s doctor` to check the protection.\n", strings.Join(open, ", "), cmd)
	return results, nil
}

// unchecked is results, or, when no check ran (nil), an unknown protection for every repository.
func unchecked(repos []repoInfo, results map[string]protection.Result) map[string]protection.Result {
	if results != nil {
		return results
	}
	results = make(map[string]protection.Result, len(repos))
	for _, r := range repos {
		results[r.name] = protection.Unknown()
	}
	return results
}

// mayBeFixedByImport says that an import may still fix a repository: its protection has findings, or could not be read. Enforcement that
// is unavailable is not fixed by one.
func mayBeFixedByImport(r protection.Result) bool {
	return r.Kind == protection.KindFindings || r.Kind == protection.KindUnknown
}

// awaitingImport says that an import may still fix a repository of results.
func awaitingImport(results map[string]protection.Result) bool {
	for _, r := range results {
		if mayBeFixedByImport(r) {
			return true
		}
	}
	return false
}

// findingCodes are the findings of a protection, comma-separated.
func findingCodes(r protection.Result) string {
	codes := make([]string, len(r.Findings))
	for i, c := range r.Findings {
		codes[i] = string(c)
	}
	return strings.Join(codes, ", ")
}

// findingWords is what each finding of a protection (D-PROTECTION) means, in words.
var findingWords = map[protection.Code]string{
	protection.MissingContractCheck: protection.ContractCheck + " is not required",
	protection.MissingTestCheck:     "the test check is not required",
	protection.NotUpToDate:          "branches need not be up to date",
	protection.AdminBypass:          "administrators can bypass it",
}

// unfinishedText says what keeps a repository from being protected: the findings of its protection, each code with its words, or that the
// protection could not be read.
func unfinishedText(r protection.Result) string {
	if len(r.Findings) == 0 {
		return "the protection could not be read"
	}
	parts := make([]string, len(r.Findings))
	for i, c := range r.Findings {
		parts[i] = string(c)
		if words, ok := findingWords[c]; ok {
			parts[i] += " (" + words + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// readProtections reads the protection of each repository and returns what it found with the lines that report it.
func readProtections(ctx context.Context, reader *github.Reader, res GitHubResult, repos []repoInfo) (map[string]protection.Result, []string) {
	results := make(map[string]protection.Result, len(repos))
	var lines []string
	for _, r := range repos {
		result := protection.Unknown()
		read, err := reader.Protection(ctx, r.name, r.branch)
		if err != nil {
			lines = append(lines, fmt.Sprintf("  %s: could not read the protection (%v)\n", r.name, err))
		} else if facts, err := read.Facts(r.private); err != nil {
			lines = append(lines, fmt.Sprintf("  %s: the protection answer is not usable (%v)\n", r.name, err))
		} else {
			result = protection.Evaluate(facts, res.AppID, res.TestChecks[r.name])
		}
		results[r.name] = result
		lines = append(lines, fmt.Sprintf("  %s: %s\n", r.name, result.Kind))
		if len(result.Findings) > 0 {
			lines = append(lines, fmt.Sprintf("    findings: %s\n", findingCodes(result)))
		}
		for _, line := range result.Instructions {
			lines = append(lines, fmt.Sprintf("    - %s\n", line))
		}
	}
	return results, lines
}

// keepProtection prints the protection the user must act on as lines that stay.
func keepProtection(scr *ui.Screen, cmd string, lines []string) {
	scr.Keep("\nProtection as GitHub reports it now:\n")
	for _, line := range lines {
		scr.Keep("%s", line)
	}
	scr.Keep("To check again at any time, run `%s doctor`.\n", cmd)
}

// runSetup runs `suiteward setup` in the serving container on a real terminal. Setup prints the TOTP enrollment URI and secret there,
// so nothing is captured or logged, and the user is told to stop session recording first. A full screen explains what happens before,
// and says how it went after. On Windows and macOS, outside an SSH session, setup runs in a window of its own while the installer waits
// on its screen; everywhere else it takes the terminal of the installer.
func runSetup(ctx context.Context, cmd string, rt Runtime, owner string, checks map[string]string, out io.Writer) error {
	argv, err := setupArgv(owner, checks)
	if err != nil {
		return err
	}
	// Setup prints the TOTP enrollment URI and secret on its terminal: with no real terminal here, that output would go
	// to a pipe or a file that a coding agent could read, so the installer does not run it.
	if !setupTerminal() {
		return errors.New("setup needs a real terminal on stdin and stdout: run the installer from your own terminal, not through a pipe, a script or a coding agent")
	}
	scr := screenOn(out)
	window := setupMachine.setupWindow(scr.FullScreen())
	// The window closes itself when setup ends on Windows, and Terminal keeps it open, cleared, on macOS.
	closes := setupMachine.goos == "windows"
	if err := explainSetup(scr, owner, window != nil, closes); err != nil {
		return err
	}
	note := ""
	if window != nil {
		started, err := setupInWindow(ctx, rt, scr, window, argv, closes)
		if started {
			return afterSetup(scr, cmd, err)
		}
		note = fmt.Sprintf("Setup could not open a window of its own (%v), so it runs in this terminal.\n", err)
	}
	return setupHere(ctx, cmd, rt, scr, out, argv, note)
}

// setupArgv is the command line of setup for the owner and the test check of each repository, in the order of the repositories, after
// the refusals that setup would make itself: the installer makes them before the container is entered.
func setupArgv(owner string, checks map[string]string) ([]string, error) {
	if !ghLoginPattern.MatchString(owner) {
		return nil, fmt.Errorf("owner %q is not a GitHub login", owner)
	}
	if len(checks) == 0 {
		return nil, errors.New("setup needs the test check of at least one repository")
	}
	repos := make([]string, 0, len(checks))
	for repo, check := range checks {
		if !repositoryName.MatchString(repo) || strings.TrimSpace(check) == "" {
			return nil, fmt.Errorf("the test check %q of %q is not a repository (owner/name) with a check name", check, repo)
		}
		if own := strings.TrimSpace(check); ownCheck(own) {
			return nil, fmt.Errorf("the test check of %s is %s, SuiteWard's own check, not the project's test check", repo, own)
		}
		if len(check) > appowner.MaxTestCheckBytes { // the limit setup enforces, so setup does not refuse after the container is entered
			return nil, fmt.Errorf("the test check of %s is longer than %d bytes", repo, appowner.MaxTestCheckBytes)
		}
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	argv := []string{"/suiteward", "setup", "--owner", owner}
	for _, repo := range repos {
		argv = append(argv, "--test-check", repo+"="+checks[repo])
	}
	return argv, nil
}

// explainSetup is the page before setup on a full screen: what happens, and that the installer never holds the secret. It waits for Enter,
// because the next thing the user sees is setup. Ctrl-C there starts nothing; the end of the input is an Enter, and setup asks again.
func explainSetup(scr *ui.Screen, owner string, window, closes bool) error {
	if !scr.FullScreen() {
		return nil
	}
	where, prompt := "Setup takes over this terminal, and the installer comes back when you press Enter after it.", "Press Enter to start setup: "
	if window {
		then := "it clears itself."
		if closes {
			then = "it clears itself and closes."
		}
		where, prompt = "Setup opens in a separate window and this one waits for it. In that window, press Enter after the code is accepted: "+then, "Press Enter to open setup in a new window: "
	}
	scr.Page("Enroll the owner",
		ui.Para(fmt.Sprintf("Next, setup makes %s the owner of this instance: the only account that can approve changes. It shows an authenticator secret (the TOTP secret) once, as text: a link that starts with otpauth:// and the same secret as a key. Add it to your authenticator app by typing the key or pasting the link, then confirm with the six-digit code from your authenticator app.", owner)),
		ui.Para("The installer never sees the secret and keeps no copy of it: setup runs in the container and writes it straight to the terminal."),
		ui.Para("Stop any screen or terminal recording first, and make sure no coding agent can read the terminal where the secret shows."),
		ui.Para(where))
	if _, err := scr.Prompt(prompt); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("wait for Enter before setup: %w", err)
	}
	return nil
}

// setupInWindow runs setup in a window of its own and waits for the window on a page of the installer. started is false when no window
// opened, and then setup has not run.
func setupInWindow(ctx context.Context, rt Runtime, scr *ui.Screen, open windowStarter, argv []string, closes bool) (started bool, err error) {
	name, args := rt.ExecTTYCommand(ContainerServe, argv)
	wait, err := open(ctx, name, args)
	if err != nil {
		return false, err
	}
	goesOn := "when setup ends"
	if closes { // the installer reads the end of the window, which waits for Enter after setup
		goesOn = "when the setup window closes"
	}
	scr.Page("Enroll the owner",
		ui.Para("Setup is open in another window. Follow it there: add the secret to your authenticator app and enter the six-digit code from your authenticator app."),
		ui.Para("This window goes on "+goesOn+". Ctrl-C stops waiting; close the setup window yourself if it stays open."))
	return true, working(scr, "waiting for the owner enrollment in the other window (Ctrl-C to cancel)", wait)
}

// setupHere hands the terminal to setup. The child needs the real terminal, and what it prints is not the screen's to keep or to draw
// again: the screen is left, and the warning is the first thing the user reads there.
func setupHere(ctx context.Context, cmd string, rt Runtime, scr *ui.Screen, out io.Writer, argv []string, note string) error {
	scr.Suspend()
	if note != "" {
		ghSay(out, "\n%s", note)
	}
	secret := "an authenticator secret (the TOTP secret, as a link and a key)"
	if scr.FullScreen() { // the page before setup has named it
		secret = "the authenticator secret (as a link and a key)"
	}
	ghSay(out, "\nSetup makes you the owner and shows %s, only on this terminal. Stop any screen recording or terminal logging now, and make sure no coding agent can read this terminal. The installer does not capture or log what setup prints.\n", secret)
	execErr := rt.ExecTTY(ctx, ContainerServe, argv)
	var waitErr error
	if scr.FullScreen() { // the screen comes back when this returns, so the secret stays on the terminal until the user has copied it
		_, waitErr = scr.Prompt("\nPress Enter to return to the installer: ")
	}
	scr.Resume() // the page that follows draws nothing on a screen that is still suspended
	if err := afterSetup(scr, cmd, execErr); err != nil {
		return err // whatever became of the wait
	}
	// The owner is enrolled: Ctrl-C or the end of the input at the wait only skips it, and must not report the enrollment as failed.
	if waitErr != nil && !errors.Is(waitErr, io.EOF) && !errors.Is(waitErr, context.Canceled) {
		return fmt.Errorf("wait for Enter after setup: %w", waitErr)
	}
	return nil
}

// afterSetup is the page the screen comes back to, so that it is not the question that was asked before setup ran, and the error of a
// setup that did not end well.
func afterSetup(scr *ui.Screen, cmd string, err error) error {
	if err != nil {
		scr.Page("Setup did not finish", ui.Para("Setup did not finish. If the owner is not enrolled yet, run `"+cmd+" enroll` to try again."))
		if errors.Is(err, errSetupTimeout) {
			err = fmt.Errorf("%w: finish it in its window, or run %s enroll again", err, cmd)
		}
		return fmt.Errorf("setup failed: %w", err)
	}
	scr.Page("Setup finished", ui.Para("The authenticator secret was shown only in setup's terminal: the installer did not see, keep or log it. From now on, approving a change needs the six-digit code from your authenticator app."))
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
