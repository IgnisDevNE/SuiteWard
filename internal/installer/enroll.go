package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// enrollTarget is the instance enroll works on: the engine that runs setup, the GitHub part of its settings and how to read
// its /status.
type enrollTarget struct {
	rt     Runtime
	github GitHub
	status func(ctx context.Context) ([]byte, error)
}

// Enroll is `suiteward enroll [--owner LOGIN] [--test-check OWNER/NAME=CHECK ...] [--settings PATH] [--tier isolated]`, the
// deliberate way to enroll the owner again, for example after losing the authenticator. It finds the instance as install and
// uninstall do (on the co-located tier the settings file must exist) and runs setup, which replaces the TOTP secret: with an owner enrolled, it asks for a typed confirmation first.
func Enroll(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newFlagSet("enroll")
	fs.String("owner", "", "the GitHub login of the instance owner (default: the enrolled owner, else asked)")
	fs.String("settings", "", "the installer settings file (default: the per-user configuration directory)")
	fs.String("tier", "", "co-located (this PC) or isolated (default: the saved settings')")
	fs.Var(&listFlag{}, "test-check", "the test check of a repository, OWNER/NAME=CHECK; repeat it (default: the saved one, else asked)")
	fs.Bool("plain", false, plainUsage)
	flags, err := parse(fs, args, env.Term.Out)
	if err != nil {
		return err
	}
	// Setup prints the TOTP secret on its terminal: with no real terminal here, it would go to a pipe or a file.
	if !setupTerminal() {
		return errors.New("enroll needs a terminal: setup prints the TOTP secret")
	}
	plain, err := wantsPlain(flags, nil)
	if err != nil {
		return err
	}
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		return err
	}
	defer endConsole(scr, &err)
	scr.Steps("Instance", "Owner")
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	settingsPath, defaultPath, err := settingsPathFrom(flags)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(settingsPath, !defaultPath)
	if err != nil {
		return err
	}
	if err := tierNeeded(r, haveSaved, env.Deps.GOOS); err != nil {
		return err
	}
	var target enrollTarget
	switch tier := r.preset("tier", string(saved.Tier)); config.Tier(tier) {
	case config.TierIsolated:
		isolated := saved
		isolated.Tier, isolated.Runtime = config.TierIsolated, RuntimePodman
		target, err = isolatedEnrollTarget(ctx, isolated, env)
	case config.TierCoLocated:
		target, err = colocatedEnrollTarget(ctx, r, env, installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved})
	default:
		err = fmt.Errorf("tier %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
	}
	if err != nil {
		return err
	}
	scr.Step(2)
	if err := enrollOwner(ctx, r, env, target); err != nil {
		return err
	}
	return holdPage(scr) // setup left the screen and it is back: its page is the last thing to read
}

// colocatedEnrollTarget finds the stack as uninstall and upgrade do: the guards of the install apply.
func colocatedEnrollTarget(ctx context.Context, r *Resolver, env Env, t installTarget) (enrollTarget, error) {
	if !t.haveSaved { // enrolling again replaces the TOTP secret: not of an instance that defaults picked
		return enrollTarget{}, fmt.Errorf("there is no settings file at %s, so nothing says which instance this is, and enrolling again replaces its TOTP secret: "+
			"run `suiteward install --upgrade`, which converts the stack and saves the settings file, or pass --settings with the file of the instance; nothing was changed", t.settingsPath)
	}
	rt, connection, err := openRuntime(ctx, r, env, t.saved, false)
	if err != nil {
		return enrollTarget{}, err
	}
	if err := guardDefaultSettings(t, rt.Name(), connection); err != nil {
		return enrollTarget{}, err
	}
	base, previous, err := existingStackFor(ctx, rt, t.saved, t.haveSaved, true)
	if err != nil {
		return enrollTarget{}, err
	}
	if !previous.Known {
		return enrollTarget{}, ErrNothingInstalled
	}
	s := base
	s.Tier, s.Runtime, s.Connection = config.TierCoLocated, rt.Name(), connection
	c := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s}}
	return enrollTarget{rt: rt, github: s.GitHub, status: c.readStatus}, nil
}

// isolatedEnrollTarget finds the installed units as the service account; the GitHub values of a host installed by hand are in
// its env file.
func isolatedEnrollTarget(ctx context.Context, s Settings, env Env) (enrollTarget, error) {
	host, err := openIsolatedHost(ctx, s, env.Deps)
	if err != nil {
		return enrollTarget{}, err
	}
	st, err := adoptUnitsDir(host.unitsDir, host.envPath, true)
	if err != nil {
		return enrollTarget{}, err
	}
	if !st.found {
		return enrollTarget{}, ErrNothingInstalled
	}
	if s.GitHub.AppID == 0 {
		if s.GitHub, err = readGitHubEnvFile(host.envPath); err != nil {
			return enrollTarget{}, err
		}
	}
	address := "http://127.0.0.1:" + strconv.Itoa(st.adopted.Settings.HTTPPort) + "/status"
	return enrollTarget{rt: host.rt, github: s.GitHub, status: func(ctx context.Context) ([]byte, error) { return env.httpGet()(ctx, address) }}, nil
}

// enrollOwner reads /status, resolves the owner, asks for the confirmation that replacing the secret of an enrolled owner needs, resolves
// the test checks and runs setup with the terminal passed through. Nothing here can answer the confirmation but the typed word.
func enrollOwner(ctx context.Context, r *Resolver, env Env, t enrollTarget) error {
	body, err := t.status(ctx)
	if err != nil {
		return fmt.Errorf("the service must be running: start it, then run enroll again (/status could not be read: %w)", err)
	}
	enrolled, err := parseEnrolledOwner(body)
	if err != nil {
		return err
	}
	def := enrolled
	if def == unknownLogin {
		def = ""
	}
	owner, err := r.Value("owner", "GitHub login of the instance owner, the only account whose approvals count", def)
	if err != nil {
		return err
	}
	if enrolled != "" {
		replaces := ""
		if enrolled != unknownLogin && !strings.EqualFold(enrolled, owner) {
			replaces = fmt.Sprintf("the owner %s with %s and ", enrolled, owner)
		}
		line, err := r.ask(fmt.Sprintf("An owner (%s) is already enrolled. Enrolling again replaces %sthe TOTP secret: the old authenticator entry stops working, and you scan a new one.\nType \"enroll\" to replace it: ", enrolled, replaces))
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read the confirmation: %w", err)
		}
		if line != "enroll" {
			return errors.New("enrolling again was not confirmed: nothing was changed")
		}
	}
	checks, err := enrollChecks(r, t.github)
	if err != nil {
		return err
	}
	if err := runSetup(ctx, t.rt, owner, checks, env.Term.Out); err != nil {
		return err
	}
	r.screen.Check("%s is enrolled as the owner.\n", owner) // the page that enroll then holds for Enter
	return nil
}

// enrollChecks is the test check of each repository of the instance: from --test-check, else from the saved settings, else asked.
func enrollChecks(r *Resolver, gh GitHub) (map[string]string, error) {
	if len(gh.Repositories) == 0 {
		return nil, errors.New("GitHub is not configured on this instance, so there is no repository to enroll for: run `suiteward install --github yes` first")
	}
	value, _ := r.given("test-check")
	listed, isList, err := parseTestCheckList(value)
	if err != nil {
		return nil, err
	}
	if value != "" && !isList {
		return nil, errors.New("--test-check is OWNER/NAME=CHECK, given once for each repository")
	}
	for repo := range listed {
		if !slices.ContainsFunc(gh.Repositories, func(name string) bool { return strings.EqualFold(name, repo) }) {
			return nil, fmt.Errorf("test-check names %s, which this instance does not serve", repo)
		}
	}
	checks := make(map[string]string, len(gh.Repositories))
	for _, repo := range gh.Repositories {
		check := lookupFold(listed, repo)
		if check == "" {
			check = lookupFold(gh.TestChecks, repo)
		}
		if check == "" {
			if check, err = r.Value("test-check."+repo, "Name of the check your CI publishes for "+repo, ""); err != nil {
				return nil, err
			}
		}
		checks[repo] = check
	}
	return checks, nil
}
