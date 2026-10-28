package installer

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// instance is the installed stack that backup init, backup and restore work on, found as install --upgrade finds it: the guards of
// the install apply, and a stack that was edited by hand stops the command.
type instance struct {
	rt             Runtime
	s              Settings // the settings of the install, with the ports and the release the stack has; what init saves
	settingsPath   string
	haveSettings   bool                                      // the settings file exists
	image          string                                    // the release image that runs
	status         func(ctx context.Context) ([]byte, error) // GET /status of the service
	parent         string                                    // where backups go unless the command names another directory
	units          map[string][]byte                         // isolated tier: the unit files as installed
	startCommand   string                                    // starts a stopped service by hand, for messages
	serviceStarted bool                                      // this run started the service, which was down: what it says of what it changed must say so
	restore        tierSteps
}

// instanceFlags adds the flags that say which instance a command works on, which every command of the backup family has.
func instanceFlags(fs *flag.FlagSet) {
	for _, f := range installFlags {
		switch f.name {
		case "settings", "token-file", "tier", "runtime", "connection":
			fs.String(f.name, "", f.usage)
		}
	}
	fs.Bool("plain", false, plainUsage)
}

// locateInstance finds the stack the flags name, on the tier of the saved settings or --tier.
func locateInstance(ctx context.Context, r *Resolver, env Env, flags map[string]string) (instance, error) {
	settingsPath, defaultPath, err := settingsPathFrom(flags)
	if err != nil {
		return instance{}, err
	}
	saved, haveSaved, err := loadSaved(env.Deps, settingsPath, !defaultPath)
	if err != nil {
		return instance{}, err
	}
	if err := tierNeeded(r, haveSaved, env.Deps.GOOS, coLocatedTierFlag); err != nil {
		return instance{}, err
	}
	switch tier := r.preset("tier", string(saved.Tier)); config.Tier(tier) {
	case config.TierIsolated:
		isolated := saved
		isolated.Tier, isolated.Runtime = config.TierIsolated, RuntimePodman
		return locateIsolated(ctx, r, env, isolated, haveSaved)
	case config.TierCoLocated:
		return locateColocated(ctx, r, env, installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved})
	default:
		return instance{}, fmt.Errorf("the installation type %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
	}
}

func locateColocated(ctx context.Context, r *Resolver, env Env, t installTarget) (instance, error) {
	rt, connection, err := openRuntime(ctx, r, env, t.saved, false)
	if err != nil {
		return instance{}, err
	}
	if err := guardDefaultSettings(t, rt.Name(), connection); err != nil {
		return instance{}, err
	}
	base, previous, err := existingStackFor(ctx, rt, t.saved, t.haveSaved, true)
	if err != nil {
		return instance{}, err
	}
	if !previous.Known {
		return instance{}, nothingInstalled(env.Deps)
	}
	s := base
	s.Tier, s.Runtime, s.Connection, s.Image = config.TierCoLocated, rt.Name(), connection, previous.Image
	if err := s.Validate(); err != nil {
		return instance{}, err
	}
	tokenFile := r.preset("token-file", "")
	if tokenFile == "" {
		if tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return instance{}, err
		}
	}
	engine, err := engineCommand(rt, env.Deps.GOOS)
	if err != nil {
		return instance{}, err
	}
	c := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s}}
	return instance{
		rt: rt, s: s, settingsPath: t.settingsPath, haveSettings: t.haveSaved, image: previous.Image, status: c.readStatus,
		parent:       backupParent(r, filepath.Dir(tokenFile)),
		startCommand: fmt.Sprintf("`%s start %s`, then, once it is healthy, `%s start %s`", engine, ContainerDB, engine, ContainerServe),
		restore:      colocatedSteps(env.Deps, rt, s, previous.Image, r.screen),
	}, nil
}

func locateIsolated(ctx context.Context, r *Resolver, env Env, s Settings, haveSaved bool) (instance, error) {
	host, err := openIsolatedHost(ctx, s, env.Deps)
	if err != nil {
		return instance{}, err
	}
	st, err := adoptUnitsDir(host.unitsDir, host.envPath, true)
	if err != nil {
		return instance{}, err
	}
	if !st.found {
		return instance{}, nothingInstalled(env.Deps)
	}
	// The ports and the release are the installed ones, whatever an older settings file says.
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort, st.adopted.Settings.JobTimeout, st.adopted.Image
	address := "http://127.0.0.1:" + strconv.Itoa(s.HTTPPort) + "/status"
	return instance{
		rt: host.rt, s: s, settingsPath: host.settingsPath, haveSettings: haveSaved, image: st.adopted.Image,
		status:       func(ctx context.Context) ([]byte, error) { return env.httpGet()(ctx, address) },
		parent:       backupParent(r, host.configDir),
		units:        st.units,
		startCommand: "systemctl --user start suiteward.service",
		restore:      isolatedSteps(host, env.Deps, r.screen),
	}, nil
}

// statusDoc is what the commands of the backup family read of GET /status. The service reports every field; a stack that does not
// is older than any release this installer upgrades.
type statusDoc struct {
	Version       string `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	Instance      string `json:"instance"`
	Owner         *struct {
		Login string `json:"login"`
	} `json:"owner"` // absent until an owner is enrolled
	GitHub []struct {
		Repo      string `json:"repo"`
		Integrity string `json:"integrity"`
	} `json:"github"`
	// A backup taken while the service did not answer records {"unavailable": true, "reason": ...} where the status would be.
	Unavailable bool   `json:"unavailable"`
	Reason      string `json:"reason"`
}

// instanceID is the id /status shows: the first 8 hex characters of the instance id, safe in a file name.
var instanceID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// plainName is a version or a tier as the installer writes them: a few characters that no terminal acts on and no shell reads as more.
var plainName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// printable makes text that came from outside, an archive or a service, safe to print on one line, also in plain output, which reaches the
// terminal as it is: it drops the control characters (what the full screen drops) and the format characters (bidirectional overrides
// among them), and turns a line break, a tab and the separators of a line and of a paragraph (U+2028, U+2029) into a space, so that a line
// of the text can never be a line of the output.
func printable(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == ' ' || r == ' ':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, text)
}

// clean makes the strings of the status that are printed safe to print.
func (d *statusDoc) clean() {
	d.Reason = printable(d.Reason)
	if d.Owner != nil {
		d.Owner.Login = printable(d.Owner.Login)
	}
	for i := range d.GitHub {
		d.GitHub[i].Repo, d.GitHub[i].Integrity = printable(d.GitHub[i].Repo), printable(d.GitHub[i].Integrity)
	}
}

// parseBackedUpStatus is parseStatus for the status of a backup, which may be the record that there was none.
func parseBackedUpStatus(body []byte) (statusDoc, error) {
	var d statusDoc
	if err := json.Unmarshal(body, &d); err == nil && d.Unavailable {
		d.clean()
		return d, nil
	}
	return parseStatus(body)
}

// parseStatus reads a status, which is text of another program: the version must be a plain name, and what is printed of the rest is cleaned.
func parseStatus(body []byte) (statusDoc, error) {
	var d statusDoc
	if err := json.Unmarshal(body, &d); err != nil {
		return statusDoc{}, errors.New("the answer of /status is not readable")
	}
	switch {
	case d.Version == "":
		return statusDoc{}, errors.New("/status shows no version")
	case !plainName.MatchString(d.Version):
		return statusDoc{}, errors.New("/status shows a version that is not a plain name (the value is not shown)")
	case !instanceID.MatchString(d.Instance):
		return statusDoc{}, errors.New("/status shows no instance id (8 hexadecimal characters): this service is older than this installer supports")
	}
	d.clean()
	return d, nil
}

// readStatus asks the running service, which must answer.
func (i instance) readStatus(ctx context.Context) ([]byte, statusDoc, error) {
	body, err := i.status(ctx)
	if err != nil {
		return nil, statusDoc{}, fmt.Errorf("could not read /status of the service (the backup records its version and instance id). Start the service with %s, then run this again: %w", i.startCommand, err)
	}
	doc, err := parseStatus(body)
	if err != nil {
		return nil, statusDoc{}, err
	}
	return body, doc, nil
}

// needSettings refuses a command that keeps something in the settings file when there is none: install first, or the upgrade that
// converts the stack saves them.
func (i instance) needSettings(d Deps) error {
	if !i.haveSettings {
		return fmt.Errorf("there is no settings file at %s to keep the recovery key in. Run `%s` first, or `%s`, which saves one", i.settingsPath, d.suggest("install"), d.suggest("install", "--upgrade"))
	}
	return nil
}

// settingsExist says whether the settings file is there.
func settingsExist(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// currentID is the id of the instance: what /status shows, else what the database holds, else "unknown" (as a backup of an instance that
// does not answer says) with what the database answered.
func (i instance) currentID(ctx context.Context) (string, error) {
	if body, err := i.status(ctx); err == nil {
		if doc, err := parseStatus(body); err == nil {
			return doc.Instance, nil
		}
	}
	return instanceFromDatabase(ctx, i.rt)
}

// instanceConfirm is what the owner confirms with the id of an instance, and how.
type instanceConfirm struct {
	id       string
	action   string // what the id confirms: "restore" or "purge"
	ofWhat   string // whose id it is, for the refusal that names a wrong one
	prompt   string // what a terminal is asked
	yesAlone bool   // --yes with the right --instance confirms on a terminal too: a purge may run from a script, a restore always has the id typed
}

// yes says whether --yes was given.
func (r *Resolver) yes() (bool, error) { return parseYesNo("yes", r.preset("yes", "false")) }

// confirmInstance is the owner typing the instance id, or --yes with --instance where nobody can type.
func (r *Resolver) confirmInstance(c instanceConfirm) error {
	yes, err := r.yes()
	if err != nil {
		return err
	}
	if !r.term.IsTerminal || (yes && c.yesAlone) {
		given := r.preset("instance", "")
		switch {
		case !yes || given == "":
			because := "the input is not a terminal"
			if r.term.IsTerminal {
				because = "`--yes` does not ask"
			}
			return fmt.Errorf("to confirm the %s, give `--yes` and `--instance %s`: nothing can ask you to type the instance id here, because %s", c.action, c.id, because)
		case given != c.id:
			return fmt.Errorf("the %s was not confirmed: `--instance %s` is not the instance id %s (%s). Nothing was changed", c.action, given, c.ofWhat, c.id)
		}
		return nil
	}
	prompt := c.prompt
	if r.screen.FullScreen() { // the page above says what is at stake, so the question stands alone and says that typing the id is a confirmation
		prompt = "Type the instance id " + c.id + " to confirm: "
	}
	line, err := r.ask(prompt)
	switch {
	case errors.Is(err, io.EOF):
		line = ""
	case err != nil:
		return fmt.Errorf("could not read your answer: %w", err)
	}
	if line != c.id {
		return fmt.Errorf("the %s was not confirmed: you did not type the instance id %s. Nothing was changed", c.action, c.id)
	}
	return nil
}

// tierSteps is what a restore does to the stack of one tier, so that the restore itself is the same for both.
type tierSteps struct {
	unavailable error                           // why the stack cannot be restored; nil when it can
	stop        func(ctx context.Context) error // stops the service, with the database still running
	clearVolume func(ctx context.Context) error // leaves the artifact volume empty
	start       func(ctx context.Context) error // migrates, then starts the service; it has its own spinner
	recovery    string                          // what to run by hand to get the service back after a restore that failed
}

// colocatedSteps: the service container is removed (a volume that a container uses cannot be), and made again by the run of its
// own spec, the one an upgrade uses, so a restore changes nothing about how the stack is run.
func colocatedSteps(d Deps, rt Runtime, s Settings, image string, scr *ui.Screen) tierSteps {
	stack, err := ColocatedStack(s, image)
	return tierSteps{
		unavailable: err,
		stop:        func(ctx context.Context) error { return rt.Remove(ctx, ContainerServe) },
		clearVolume: func(ctx context.Context) error {
			if err := rt.Delete(ctx, KindVolume, VolumeData); err != nil {
				return err
			}
			return rt.Create(ctx, KindVolume, VolumeData)
		},
		start: func(ctx context.Context) error {
			return working(scr, "Updating the database and starting the service", func() error {
				if err := rt.Run(ctx, stack.Migrate); err != nil {
					return fmt.Errorf("the database update (migration) failed, so the service was not started: %w", err)
				}
				return rt.Run(ctx, stack.Serve)
			})
		},
		recovery: "run `" + d.suggest("install") + "` with the same options as this run: it creates the service again from your settings, and your data and secrets are kept",
	}
}

// isolatedSteps: both units stop, so that the start runs the migration again; the Quadlet unit of the volume makes it again, empty.
func isolatedSteps(host isolatedHost, d Deps, scr *ui.Screen) tierSteps {
	systemctl := userSystemctl(d)
	return tierSteps{
		stop: func(ctx context.Context) error {
			_, err := systemctl(ctx, "stop", "suiteward.service", "suiteward-migrate.service")
			return err
		},
		clearVolume: func(ctx context.Context) error {
			if err := host.rt.Delete(ctx, KindVolume, VolumeData); err != nil {
				return err
			}
			_, err := systemctl(ctx, "restart", "suiteward-data-volume.service")
			return err
		},
		start:    func(ctx context.Context) error { return migrateAndStart(ctx, d, scr) },
		recovery: "run `systemctl --user start suiteward.service`",
	}
}

// owner is the login of the enrolled owner, "" when none is.
func (d statusDoc) owner() string {
	if d.Owner == nil {
		return ""
	}
	return d.Owner.Login
}

func (d statusDoc) repositories() []string {
	repos := make([]string, len(d.GitHub))
	for i, g := range d.GitHub {
		repos[i] = g.Repo
	}
	return repos
}

// changed says what a command that stops half way has changed of this instance, for the error that says it stopped.
func (i instance) changed() string {
	if i.serviceStarted {
		return "the service that this run started is running, and nothing else was changed"
	}
	return "nothing was changed"
}
