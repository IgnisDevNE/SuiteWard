package installer

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// instance is the installed stack that backup init, backup and restore work on, found as install --upgrade finds it: the guards of
// the install apply, and a stack that was edited by hand stops the command.
type instance struct {
	rt           Runtime
	s            Settings // the settings of the install, with the ports and the release the stack has; what init saves
	settingsPath string
	haveSettings bool                                      // the settings file exists
	image        string                                    // the release image that runs
	status       func(ctx context.Context) ([]byte, error) // GET /status of the service
	parent       string                                    // where backups go unless the command names another directory
	units        map[string][]byte                         // isolated tier: the unit files as installed
	startCommand string                                    // starts a stopped service by hand, for messages
	restore      tierSteps
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
	saved, haveSaved, err := loadSaved(settingsPath)
	if err != nil {
		return instance{}, err
	}
	if err := tierNeeded(r, haveSaved, env.Deps.GOOS); err != nil {
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
		return instance{}, fmt.Errorf("tier %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
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
		return instance{}, ErrNothingInstalled
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
		restore:      colocatedSteps(rt, s, previous.Image, r.screen),
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
		return instance{}, ErrNothingInstalled
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
}

// instanceID is the id /status shows: the first 8 hex characters of the instance id, safe in a file name.
var instanceID = regexp.MustCompile(`^[0-9a-f]{8}$`)

func parseStatus(body []byte) (statusDoc, error) {
	var d statusDoc
	if err := json.Unmarshal(body, &d); err != nil {
		return statusDoc{}, errors.New("the answer of /status is not JSON")
	}
	switch {
	case d.Version == "":
		return statusDoc{}, errors.New("/status reports no version")
	case !instanceID.MatchString(d.Instance):
		return statusDoc{}, errors.New("/status reports no instance id (8 hexadecimal characters): this service is older than the installer supports")
	}
	return d, nil
}

// readStatus asks the running service, which must answer.
func (i instance) readStatus(ctx context.Context) ([]byte, statusDoc, error) {
	body, err := i.status(ctx)
	if err != nil {
		return nil, statusDoc{}, fmt.Errorf("read GET /status of the service (its version and instance id belong in the backup): start it (%s) and run this again: %w", i.startCommand, err)
	}
	doc, err := parseStatus(body)
	if err != nil {
		return nil, statusDoc{}, err
	}
	return body, doc, nil
}

// needSettings refuses a command that keeps something in the settings file when there is none: install first, or the upgrade that
// converts the stack saves them.
func (i instance) needSettings() error {
	if !i.haveSettings {
		return fmt.Errorf("there are no settings at %s to keep the recovery key in: install first, or run `suiteward install --upgrade`, which saves them", i.settingsPath)
	}
	return nil
}

// settingsExist says whether the settings file is there.
func settingsExist(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
func colocatedSteps(rt Runtime, s Settings, image string, scr *ui.Screen) tierSteps {
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
			return working(scr, "Running the migration and starting the service", func() error {
				if err := rt.Run(ctx, stack.Migrate); err != nil {
					return fmt.Errorf("the migration failed, so the service was not started: %w", err)
				}
				return rt.Run(ctx, stack.Serve)
			})
		},
		recovery: "run `suiteward install` with the options of this run: it creates the service again from the settings, and the volumes and the secrets are kept",
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
