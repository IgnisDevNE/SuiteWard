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
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
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
	fs.String("owner", "", "the GitHub username of the owner (default: the enrolled owner, else asked)")
	fs.String("settings", "", "the file where the installer keeps its settings (default: your user configuration folder)")
	fs.String("tier", "", "installation type: co-located (on this computer, next to your coding agent) or isolated (on a separate server) (default: the one in the saved settings)")
	fs.Var(&listFlag{}, "test-check", "the name of the check your CI publishes, as OWNER/NAME=CHECK; repeat the flag for more repositories (default: the saved one, else asked)")
	fs.Bool("plain", false, plainUsage)
	flags, err := parse(fs, args, env.Term.Out)
	if err != nil {
		return err
	}
	// Setup prints the TOTP secret on its terminal: with no real terminal here, it would go to a pipe or a file.
	if !setupTerminal() {
		return errors.New("enroll needs a real terminal, because it shows the authenticator secret (the TOTP secret) there, and nothing was changed: run it from your own terminal, not through a pipe or a script")
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
	if err := r.chooseInstallation(env.Deps); err != nil {
		return err
	}
	env.Deps.instance = r.instance()
	settingsPath, defaultPath, err := settingsPathFrom(r)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(env.Deps, settingsPath, !defaultPath)
	if err != nil {
		return err
	}
	// Enroll refuses a co-located stack with no settings file (colocatedEnrollTarget), so that is not what a host that has none is offered.
	if err := tierNeeded(r, haveSaved, env.Deps.GOOS, "run `"+env.Deps.suggest("install", "--upgrade --tier co-located")+"` first, which saves the settings file of a co-located installation"); err != nil {
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
		err = fmt.Errorf("the installation type %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
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
	if !t.haveSaved { // enrolling again replaces the authenticator secret: not of an instance that defaults picked
		return enrollTarget{}, fmt.Errorf("there is no settings file at %s, so nothing says which instance this is, and enrolling again replaces that instance's authenticator secret (the TOTP secret). Nothing was changed: "+
			"run `%s`, which saves the settings file, or pass --settings with the file of the instance", t.settingsPath, env.Deps.suggest("install", "--upgrade"))
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
		return enrollTarget{}, nothingInstalled(env.Deps)
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
		return enrollTarget{}, nothingInstalled(env.Deps)
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
		return fmt.Errorf("the service must be running: start it, then run enroll again (its status could not be read: %w)", err)
	}
	enrolled, err := parseEnrolledOwner(body)
	if err != nil {
		return err
	}
	def := enrolled
	if def == unknownLogin {
		def = ""
	}
	r.explain([]string{"owner"}, "Instance owner", ownerPage)
	owner, err := r.Value("owner", "GitHub username of the owner, the only account that can approve changes", def)
	if err != nil {
		return err
	}
	if enrolled != "" {
		if err := confirmEnrollAgain(r, enrolled, owner); err != nil {
			return err
		}
	}
	checks, err := enrollChecks(env.Deps, r, t.github)
	if err != nil {
		return err
	}
	if err := runSetup(ctx, env.Deps, t.rt, owner, checks, env.Term.Out); err != nil {
		return err
	}
	r.screen.Check("%s is enrolled as the owner.\n", owner) // the page that enroll then holds for Enter
	return nil
}

// confirmEnrollAgain is the typed word that replacing the authenticator secret of an enrolled owner needs. On a full screen it has a page that says
// what is replaced and that the word is a confirmation; a plain screen asks the one question it always asked.
func confirmEnrollAgain(r *Resolver, enrolled, owner string) error {
	replaces := ""
	if enrolled != unknownLogin && !strings.EqualFold(enrolled, owner) {
		replaces = fmt.Sprintf("the owner %s with %s and ", enrolled, owner)
	}
	what := fmt.Sprintf("An owner (%s) is already enrolled. Enrolling again replaces %sthe authenticator secret (the TOTP secret): the old entry in your authenticator app stops working, and you add the new secret that setup shows to your app.", enrolled, replaces)
	prompt := what + "\nType \"enroll\" to replace it: "
	if r.screen.FullScreen() {
		r.screen.Page("Enroll the owner again", ui.Para(what), ui.Para("Nothing changes until you type enroll."))
		prompt = `Type "enroll" to confirm: `
	}
	line, err := r.ask(prompt)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the confirmation: %w", err)
	}
	if line != "enroll" {
		return errors.New("enrolling again was not confirmed: nothing was changed")
	}
	return nil
}

// enrollChecks is the test check of each repository of the instance: from --test-check, else from the saved settings, else asked.
func enrollChecks(d Deps, r *Resolver, gh GitHub) (map[string]string, error) {
	if len(gh.Repositories) == 0 {
		return nil, fmt.Errorf("GitHub is not set up on this instance, so there is no repository to enroll for: run `%s` first", d.suggest("install", "--github yes"))
	}
	value, _ := r.given("test-check")
	listed, isList, err := parseTestCheckList(value)
	if err != nil {
		return nil, err
	}
	if value != "" && !isList {
		return nil, errors.New("--test-check takes OWNER/NAME=CHECK, once for each repository")
	}
	for repo := range listed {
		if !slices.ContainsFunc(gh.Repositories, func(name string) bool { return strings.EqualFold(name, repo) }) {
			return nil, fmt.Errorf("--test-check names %s, which this instance does not protect", repo)
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
