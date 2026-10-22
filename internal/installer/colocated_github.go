package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// githubInstall is the GitHub part of an install, which both tiers run: the flow with its settings, and the enrollment of the
// owner once the stack is up.
type githubInstall struct {
	rt  Runtime
	env Env
	out io.Writer
	s   Settings // the GitHub flow changes its GitHub part

	pending *PendingApp // the App an earlier run created and did not finish with

	githubKey   []byte // the App key to store, nil when its secret exists or GitHub is off
	setupOwner  string // the instance owner, when the GitHub flow ran: the login setup enrolls
	setupChecks map[string]string
}

func (g githubInstall) say(format string, args ...any) { _, _ = fmt.Fprintf(g.out, format, args...) }

// githubAliases are the flags and answers-file keys of the existing App route (the ones local.ps1 had), mapped to the
// keys the GitHub flow reads. Both spellings work in a flag and in the answers file.
var githubAliases = map[string]string{
	"github-app-id":          "app-id",
	"github-installation-id": "installation-id",
	"github-owner":           "owner",
	"github-repositories":    "repositories",
	"github-app-key-file":    "app-key-file",
	"github-app-key":         "app-key", // the isolated tier's answers-file key for the PEM
}

// aliasGitHubKeys copies each alias to the key the GitHub flow reads, unless that key is set.
func aliasGitHubKeys(m map[string]string) {
	for old, key := range githubAliases {
		if v, ok := m[old]; ok {
			if _, set := m[key]; !set {
				m[key] = v
			}
		}
	}
}

// githubImplying are the answers that only make sense with a GitHub App: given without one, they are a mistake that
// local.ps1 refused too.
var githubImplying = []string{"installation-id", "repositories", "owner", "app-key-file", "app-key", "github-account", "manifest-code", "test-check", "ruleset-imported"}

func (e Env) githubFlow() func(context.Context, githubEnv) (GitHubResult, error) {
	if e.ResolveGitHub != nil {
		return e.ResolveGitHub
	}
	return resolveGitHub
}

// wantsGitHub decides whether the GitHub flow runs. It does when an App id is given, when GitHub is asked for (--github
// yes, or a yes at the prompt of a terminal), or when the saved settings have GitHub but the key secret is gone. A
// re-run with settings that are complete does not ask GitHub again. GitHub values without an App are an error.
func (g *githubInstall) wantsGitHub(ctx context.Context, r *Resolver) (bool, error) {
	if id, ok := r.given("app-id"); ok {
		if strings.EqualFold(strings.TrimSpace(id), "none") {
			g.s.GitHub = GitHub{}
			return false, nil
		}
		return true, nil
	}
	existing := g.s.GitHub.AppID != 0
	if v, ok := r.given("github"); ok {
		yes, err := parseYesNo("github", v)
		return yes, err
	}
	for _, key := range githubImplying {
		if _, ok := r.given(key); ok && !existing {
			return false, fmt.Errorf("GitHub values were given without a GitHub App (%s): pass --github-app-id with --github-installation-id and the App key for an App that exists, or --github yes to create one, or --github-app-id none", key)
		}
	}
	for _, key := range githubImplying {
		if _, ok := r.given(key); ok {
			return true, nil // values for the GitHub App of the saved settings
		}
	}
	if existing {
		have, err := g.rt.Secrets().Exists(ctx, SecretGitHubAppKey)
		return !have, err
	}
	if !r.term.IsTerminal {
		return false, nil
	}
	return r.confirm("github", "Set up GitHub now (creates a GitHub App in your browser)?")
}

// githubFlow runs the GitHub flow when it is wanted and applies its result: the settings, the App key and the owner login
// that setup enrolls.
func (g *githubInstall) githubFlow(ctx context.Context, r *Resolver, settingsDir string) error {
	run, err := g.wantsGitHub(ctx, r)
	if err != nil || !run {
		return err
	}
	port := 0
	if v, ok := r.given("manifest-port"); ok {
		if port, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("manifest-port %q is not a number", v)
		}
	}
	secrets := g.rt.Secrets()
	stored := false
	existing := g.s.GitHub
	res, err := g.env.githubFlow()(ctx, githubEnv{
		R: r, Out: g.out, Tier: g.s.Tier, SettingsDir: settingsDir, ManifestPort: port, Existing: &existing,
		KeyExists: func(ctx context.Context) (bool, error) { return secrets.Exists(ctx, SecretGitHubAppKey) },
		ReadKey:   func(ctx context.Context) ([]byte, error) { return secrets.Read(ctx, SecretGitHubAppKey) },
		StoreKey: func(ctx context.Context, key []byte) error {
			if err := secrets.Write(ctx, SecretGitHubAppKey, key, false); err != nil {
				return err
			}
			stored = true
			return nil
		},
	})
	if err != nil {
		return err
	}
	if res.AppID == 0 || res.InstallationID == 0 || len(res.Repositories) == 0 {
		return errors.New("the GitHub flow returned no App, installation or repositories")
	}
	g.s.GitHub = GitHub{
		AppID: res.AppID, InstallationID: res.InstallationID, Owner: res.Account, Repositories: res.Repositories, TestChecks: res.TestChecks,
	}
	if err := g.s.Validate(); err != nil {
		return err
	}
	g.setupOwner, g.setupChecks = res.OwnerLogin, res.TestChecks
	if !stored && len(res.PrivateKey) > 0 { // an App that exists: its key came from a file or the answers, and is stored now
		have, err := secrets.Exists(ctx, SecretGitHubAppKey)
		if err != nil {
			return err
		}
		if !have {
			g.githubKey = res.PrivateKey
		}
	}
	return nil
}

// statusTries and statusEvery bound the wait for /status of a service that has just started.
const (
	statusTries = 30
	statusEvery = time.Second
)

// enrolledOwner reads /status of the service, which may still be starting, and returns the login of the enrolled owner, or
// "" when there is none. An answer it cannot read is an error: it is not "no owner".
func (g *githubInstall) enrolledOwner(ctx context.Context, status func(context.Context) ([]byte, error)) (string, error) {
	var body []byte
	var err error
	for i := range statusTries {
		if body, err = status(ctx); err == nil || ctx.Err() != nil {
			break
		}
		if i < statusTries-1 {
			g.env.Sleep(statusEvery)
		}
	}
	if err != nil {
		return "", err
	}
	var parsed struct {
		Owner *struct{ Login string } `json:"owner"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", errors.New("the answer of /status is not JSON")
	}
	switch {
	case parsed.Owner == nil:
		return "", nil
	case parsed.Owner.Login == "":
		return "unknown login", nil
	}
	return parsed.Owner.Login, nil
}

// enroll runs setup on a real terminal, and otherwise prints the command to run on one. It never runs setup over an owner that is
// enrolled, nor when it cannot tell: setup enrolls again, which replaces the owner's TOTP secret.
func (g *githubInstall) enroll(ctx context.Context, status func(context.Context) ([]byte, error)) error {
	cmd, err := engineCommand(g.rt, g.env.Deps.GOOS)
	if err != nil {
		return err
	}
	repos := make([]string, 0, len(g.setupChecks))
	for repo := range g.setupChecks {
		repos = append(repos, repo)
	}
	slices.Sort(repos)
	var checks []string
	for _, repo := range repos {
		checks = append(checks, "--test-check '"+repo+"="+g.setupChecks[repo]+"'")
	}
	owner := g.setupOwner
	if owner == "" { // a run that took the App from the env file knows neither the owner nor the checks
		owner, checks = "<login>", []string{"--test-check '<owner/name>=<check name>'"}
	}
	command := fmt.Sprintf("%s exec -it %s /suiteward setup --owner %s %s", cmd, ContainerServe, owner, strings.Join(checks, " "))
	login, err := g.enrolledOwner(ctx, status)
	switch {
	case err != nil:
		g.say("Setup was not run: /status of the service could not be read (%v), so it is not known whether an owner is enrolled, and setup would replace the TOTP secret of one. Once the service answers, check /status, and if it has no owner, run on a real terminal with session recording off:\n  %s\n", err, command)
	case login != "":
		g.say("Note: an owner (%s) is already enrolled; setup was skipped, so the TOTP secret is unchanged. To re-enroll (for example after losing the authenticator), run on a real terminal with session recording off: %s\n", login, command)
	case g.setupOwner == "":
		g.say("Note: no owner is enrolled, and this run does not know the owner login or the test checks. Turn off any session recording of a real terminal, then enroll the owner (replace <login> and the check):\n  %s\n", command)
	case setupTerminal():
		if err := runSetup(ctx, g.rt, g.setupOwner, g.setupChecks, g.out); err != nil {
			return fmt.Errorf("the stack is running, but enrolling the owner failed: %w", err)
		}
	default:
		g.say("Next: setup needs a real terminal (it prints the TOTP secret). Turn off any session recording of that terminal, then enroll the owner:\n  %s\n", command)
	}
	return nil
}
