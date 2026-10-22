package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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

	pendingPath string      // where an App this run creates is recorded; empty when no run of the flow is possible
	pending     *PendingApp // the App an earlier run created and did not finish with

	githubKey   []byte // the App key to store, nil when its secret exists or GitHub is off
	setupOwner  string // the instance owner, when the GitHub flow ran: the login setup enrolls
	setupChecks map[string]string
	enrollHint  string // the command the messages name to enroll the owner (enrollCommand)
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
	existing := g.s.GitHub.AppID != 0 || g.pending != nil
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
	if g.pending != nil && g.s.GitHub.AppID == 0 { // an App an earlier run created and did not finish with; once the settings name an App, the record is not used
		return true, nil
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
	if pending, found, err := loadPendingApp(g.pendingPath); err != nil {
		return err
	} else if found {
		g.pending = &pending
	}
	run, err := g.wantsGitHub(ctx, r)
	if err != nil || !run {
		if err == nil && g.pending != nil { // GitHub is off in this run, but the App an earlier run created is still there
			g.say("Note: GitHub is off in this run, and the App %s that an earlier run created is not used. It still exists on GitHub (%s) and its key is the secret %s (%s); the record is %s. A re-run without --github no takes it up again (or pass --github yes). To throw it away, delete the App, remove the secret and delete the record.\n",
				g.pending.Slug, appPage("https://github.com", g.pending.Slug, g.pending.Account, g.pending.AccountType), SecretGitHubAppKey, g.removeKeySecret(), g.pendingPath)
		}
		return err
	}
	port := 0
	if v, ok := r.given("manifest-port"); ok {
		if port, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("manifest-port %q is not a number", v)
		}
	}
	secrets := g.rt.Secrets()
	stored, recorded := false, false
	existing := g.s.GitHub
	res, err := g.env.githubFlow()(ctx, githubEnv{
		R: r, Out: g.out, Tier: g.s.Tier, SettingsDir: settingsDir, ManifestPort: port, Existing: &existing, Pending: g.pending,
		RecordApp: func(_ context.Context, app PendingApp) error {
			if err := savePendingApp(g.pendingPath, app); err != nil {
				return err
			}
			recorded = true
			return nil
		},
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
		return g.leftovers(err, stored, recorded)
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

// removeKeySecret is how to remove the App key secret with the runtime of this stack.
func (g *githubInstall) removeKeySecret() string {
	if _, isDocker := g.rt.(*Docker); isDocker { // Docker keeps one file per consumer in named volumes
		if spec, _, err := secretFor(SecretGitHubAppKey, ContainerServe); err == nil {
			return fmt.Sprintf("delete the file %s from the Docker volume %s", spec.file, secretVolume(ContainerServe))
		}
	}
	if cmd, err := engineCommand(g.rt, g.env.Deps.GOOS); err == nil {
		return cmd + " secret rm " + SecretGitHubAppKey
	}
	return "remove it from the secret store of the container runtime"
}

// leftovers adds to the error of a failed flow what the run left behind and what to do about it: the App, its key secret
// and the record of it are named, with how to remove each, so that the next run neither fails on the secret nor creates a
// second App. The flow's own error names the page that deletes the App.
func (g *githubInstall) leftovers(err error, stored, recorded bool) error {
	secret := SecretGitHubAppKey + " (" + g.removeKeySecret() + ")"
	switch {
	case errors.Is(err, ErrSecretExists):
		return fmt.Errorf("%w. The secret %s already exists, so the key of the App this run created was not stored: an earlier run left it behind. Delete the App that this run created (the link above) and the one of the earlier run on GitHub, remove the secret %s, then run the installer again", err, SecretGitHubAppKey, secret)
	case errors.Is(err, errPendingKeyMissing) && g.pending != nil:
		return fmt.Errorf("%w. The key secret %s is not in the secret store (it was removed or purged). Either delete that App (the link above) and the record %s, then run the installer again; or install the App if it is not installed yet, create a new private key on its settings page and run again with --github-app-id %d, --github-installation-id <the number in the address of the installation's settings page> and --github-app-key-file <the key file>", err, SecretGitHubAppKey, g.pendingPath, g.pending.AppID)
	case errors.Is(err, errResuming) && g.pending != nil:
		return fmt.Errorf("%w. This run took up the App %s, which an earlier run recorded in %s; its key is the secret %s. Run the installer again to retry. A 401 can also mean a wrong system clock or a key that was replaced on GitHub: check those first. If the App no longer exists (404 above), or to start over, delete the old App on GitHub if it is still there, delete the record file and remove the secret, then run again to create a new App", err, g.pending.Slug, g.pendingPath, secret)
	case stored && recorded:
		return fmt.Errorf("%w. The key of the App is in the secret store as %s and the App is recorded in %s: run the installer again to take up that App. To start over instead, delete the App (the link above), remove the secret and delete the record", err, secret, g.pendingPath)
	case stored:
		return fmt.Errorf("%w. The key of the App is in the secret store as %s, and no record of the App was made: delete the App (the link above) and remove the secret before you run the installer again", err, secret)
	}
	return err
}

// clearPending deletes the record of an App once the settings, which name an App, are saved. A record that stays is not used again
// (the settings come first), so a failure is only reported.
func (g *githubInstall) clearPending() {
	if g.pendingPath == "" || g.s.GitHub.AppID == 0 { // a run without GitHub keeps the record: the App and its key are still there
		return
	}
	if err := os.Remove(g.pendingPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		g.say("Note: %v. The settings name the App now, so the record is not used again: you may delete it.\n", err)
	}
}

// statusTries and statusEvery bound the wait for /status of a service that has just started.
const (
	statusTries = 30
	statusEvery = time.Second
)

// unknownLogin stands for an owner that /status reports without a login.
const unknownLogin = "unknown login"

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
	return parseEnrolledOwner(body)
}

// parseEnrolledOwner is the login of the owner /status reports, or "" when it reports none.
func parseEnrolledOwner(body []byte) (string, error) {
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
		return unknownLogin, nil
	}
	return parsed.Owner.Login, nil
}

// enrollCommand is the command that enrolls the owner again, as the messages of the installer name it: the tier and the
// settings file are named when they are not the defaults.
func enrollCommand(r *Resolver, isolated bool) string {
	command := "suiteward enroll"
	if isolated {
		command += " --tier isolated"
	}
	if path, ok := r.given("settings"); ok {
		command += " --settings " + path
	}
	return command
}

// enroll runs setup on a real terminal, and otherwise prints the command to run on one. It never runs setup over an owner that is
// enrolled, nor when it cannot tell: setup enrolls again, which replaces the owner's TOTP secret.
func (g *githubInstall) enroll(ctx context.Context, status func(context.Context) ([]byte, error)) error {
	command := g.enrollHint
	login, err := g.enrolledOwner(ctx, status)
	switch {
	case err != nil:
		g.say("Setup was not run: /status of the service could not be read (%v), so it is not known whether an owner is enrolled, and setup would replace the TOTP secret of one. Once the service answers, check /status, and if it has no owner, run on a real terminal with session recording off:\n  %s\n", err, command)
	case login != "":
		g.say("Note: an owner (%s) is already enrolled; setup was skipped, so the TOTP secret is unchanged. To re-enroll (for example after losing the authenticator), run on a real terminal with session recording off: %s\n", login, command)
	case g.setupOwner == "":
		g.say("Note: no owner is enrolled, and this run does not know the owner login or the test checks. Turn off any session recording of a real terminal, then enroll the owner:\n  %s\n", command)
	case setupTerminal():
		if err := runSetup(ctx, g.rt, g.setupOwner, g.setupChecks, g.out); err != nil {
			return fmt.Errorf("the stack is running, but enrolling the owner failed: %w", err)
		}
	default:
		g.say("Next: setup needs a real terminal (it prints the TOTP secret). Turn off any session recording of that terminal, then enroll the owner:\n  %s\n", command)
	}
	return nil
}
