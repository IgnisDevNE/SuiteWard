package installer

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The service-account half of the isolated install, run as the account `sudo suiteward install host` prepared and never as
// root. installIsolated(ctx, s, r, env) takes the parsed settings, the Resolver (flags, answers file, prompts), the
// machine's Env (its Deps, the writer for its report and the GitHub flow). It reads these answers: "image" (sha256:<index
// digest> or the full reference; optional when this binary carries a release or an install is adopted) and the answers of the
// GitHub flow, the same as the co-located tier takes (colocated_github.go): "github", "github-app-id" with
// "github-installation-id", "github-repositories" and the App key ("github-app-key-file", a path or - for stdin, or
// "github-app-key" in the answers file, never a flag), "github-owner", "test-check" and "manifest-port"; and "http-port",
// "mcp-port" and "job-timeout", which an installed stack replaces (and the run reports it). Every external command goes
// through Deps.Exec.
//
// The order is fixed: checks, the ports and the adoption guard first, so a refusal changes nothing; then the images are pulled
// and the GitHub flow runs and is checked; then secrets, the GitHub env file, units, the generator dry run, the database, the
// application role, the service, the wait for it to be stable, the settings file, and last setup.

// Health wait of the database, variables so tests do not sleep.
var (
	dbHealthTimeout  = 90 * time.Second
	dbHealthInterval = 2 * time.Second
)

// generatorPath is the Podman quadlet generator, whose dry run lists every unit it reads.
const generatorPath = "/usr/lib/systemd/system-generators/podman-system-generator"

// isolatedHost is the service account's machine as the isolated commands find it: the checks passed and the paths are known.
type isolatedHost struct {
	d            Deps
	rt           Runtime
	run          func(name string, args ...string) (string, error)
	configDir    string
	unitsDir     string
	settingsPath string
	envPath      string // the GitHub env file of the account
}

// openIsolatedHost refuses what the isolated commands refuse before they look at anything else: a system that is not Linux,
// root or another account, invalid settings, a Podman that is not rootless and a user manager that is not ready.
func openIsolatedHost(ctx context.Context, s Settings, d Deps) (isolatedHost, error) {
	if err := d.validate(); err != nil {
		return isolatedHost{}, err
	}
	if d.GOOS != "linux" {
		return isolatedHost{}, fmt.Errorf("the isolated tier runs on Linux, this system is %s", d.GOOS)
	}
	run := func(name string, args ...string) (string, error) {
		b, err := d.Exec(ctx, name, args, nil)
		return strings.TrimSpace(string(b)), err
	}
	home, err := checkAccount(run, d.Getenv("HOME"))
	if err != nil {
		return isolatedHost{}, err
	}
	if err := s.Validate(); err != nil {
		return isolatedHost{}, err
	}
	rt, err := NewPodman(ctx, d, s.Connection)
	if err != nil {
		return isolatedHost{}, err
	}
	if err := checkUserManager(run); err != nil {
		return isolatedHost{}, err
	}
	configDir := filepath.Join(home, ".config", "suiteward")
	return isolatedHost{
		d: d, rt: rt, run: run, configDir: configDir, unitsDir: filepath.Join(home, ".config", "containers", "systemd"),
		settingsPath: filepath.Join(configDir, "settings.yaml"), envPath: filepath.Join(configDir, "github.env"),
	}, nil
}

// answerSource says where the answer of key came from: its flag or its key of the answers file; empty when it was not given.
func answerSource(r *Resolver, key string) string {
	if _, ok := r.flags[key]; ok {
		return "--" + key
	}
	if _, ok := r.answers[key]; ok {
		return `"` + key + `" in the answers file`
	}
	return ""
}

// askedSettings resolves the ports and the job timeout over base (the flag, the answers file, else base) and validates the
// result; an invalid value is refused naming the flag or the answers-file key that gave it. It never prompts: the isolated install
// keeps the values of an installed stack unless they are given (adoptInstalled), an upgrade never asks, and an install asks only
// what a person at the terminal must decide. Both tiers use it to see what an upgrade is asked for.
func askedSettings(r *Resolver, base Settings) (Settings, error) {
	s := base
	port := func(key string, def int) (int, error) {
		v := r.preset(key, strconv.Itoa(def))
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("%s %q is not a number", answerSource(r, key), v) // the default of a key that was not given is one
		}
		return n, nil
	}
	var err error
	if s.HTTPPort, err = port("http-port", base.HTTPPort); err != nil {
		return Settings{}, err
	}
	if s.MCPPort, err = port("mcp-port", base.MCPPort); err != nil {
		return Settings{}, err
	}
	s.JobTimeout = r.preset("job-timeout", base.JobTimeout)
	if err := s.Validate(); err != nil {
		var from []string
		for _, key := range []string{"http-port", "mcp-port", "job-timeout"} {
			if source := answerSource(r, key); source != "" {
				from = append(from, source)
			}
		}
		if len(from) > 0 {
			err = fmt.Errorf("%w (asked for with %s)", err, strings.Join(from, ", "))
		}
		return Settings{}, err
	}
	return s, nil
}

func installIsolated(ctx context.Context, s Settings, r *Resolver, env Env) error {
	d, out, scr := env.Deps, env.Term.Out, r.screen
	scr.Steps(isolatedInstallSteps...)
	s, err := askedSettings(r, s)
	if err != nil {
		return err
	}
	host, err := openIsolatedHost(ctx, s, d)
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	rt, configDir, unitsDir, settingsPath := host.rt, host.configDir, host.unitsDir, host.settingsPath

	// The adoption guard and the image, before anything is changed.
	neverStarted := func() (bool, error) { return stackNeverStarted(host.run, settingsPath) }
	digest, installed, err := adoptInstalled(env.Deps.command(), &s, unitsDir, filepath.Join(configDir, "github.env"), r, neverStarted, scr.Warn) // what it changes is a notice to keep
	if err != nil {
		return err
	}
	oldEnvFile := installed.oldEnv
	// An App the settings do not name, but the env file does (a host installed by hand, or a run that stopped after it wrote the
	// file), is the App of this install: the flow must not offer to create another.
	var fromFile GitHub
	if s.GitHub.AppID == 0 {
		if fromFile, err = readGitHubEnvFile(filepath.Join(configDir, "github.env")); err != nil {
			return err
		}
		s.GitHub = fromFile
	}
	// Every port this run publishes is free, or the install stops; the listener on a port of the installed units is the service of
	// the stack itself while it runs.
	own, err := ownedPorts(ctx, d, installed)
	if err != nil {
		return err
	}
	if err := checkPortsFree(ctx, d, s, own, scr.Warn); err != nil {
		return err
	}

	// Every input, resolved and checked: the images first, so a wrong digest fails before the flow creates an App.
	scr.Step(2)
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	if err := pullMissing(ctx, scr, rt, postgres, releaseImageRepo+"@"+digest); err != nil {
		return err
	}
	scr.Step(3)
	g := &githubInstall{rt: rt, env: env, out: out, s: s, pendingPath: pendingAppPath(settingsPath), enrollHint: enrollCommand(env.Deps.command(), r, true)}
	if err := g.githubFlow(ctx, r, configDir); err != nil {
		return err
	}
	if err := g.checkEnvFileHoldsNoApp(host.envPath); err != nil {
		return err
	}
	if err := g.checkAppKey(ctx, host.envPath); err != nil {
		return err
	}

	// Changes begin here. The env file comes before the secrets: when the flow just created the App (its key is stored already), a
	// failure in between must still leave a file for a re-run to take the App from.
	scr.Step(4)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", configDir, err)
	}
	gh := g.s.GitHub // an env file that already holds these values stays as its author wrote it
	inFile := gh.AppID == fromFile.AppID && gh.InstallationID == fromFile.InstallationID && slices.Equal(gh.Repositories, fromFile.Repositories)
	envChanged, err := writeEnvFile(filepath.Join(configDir, "github.env"), gh, gh.AppID != 0 && !inFile)
	if err != nil {
		return err
	}
	keyStored := g.githubKey != nil // a running service has the key secret of its last start mounted
	if err := ensureSecrets(ctx, rt.Secrets(), g.githubKey); err != nil {
		return err
	}
	units, err := prepareUnits(ctx, host, d, scr, UnitParamsFrom(g.s, digest), false)
	if err != nil {
		return err
	}

	// A running service reads its unit, its env file and its key secret when it starts: it is restarted when this run changed
	// one of them, and before setup enrolls the owner, which must not happen on a service that cannot reach GitHub yet.
	scr.Step(5)
	leftRunning, err := startStack(ctx, rt, d, scr, installed.mustRestart(units, envChanged, keyStored))
	if err != nil {
		return err
	}
	if err := waitStable(ctx, env, scr, g.s.HTTPPort); err != nil {
		return err
	}
	if err := SaveSettings(settingsPath, g.s); err != nil {
		return err
	}
	g.clearPending() // the settings name the App now
	scr.Step(6)
	say("\nSuiteWard %s is running on this account. Settings are saved in %s.\n\n", digest, settingsPath)
	if gh.AppID == 0 {
		say("GitHub is not configured in the settings of this run: if the service should reach GitHub, run the install again with --github yes (or the GitHub values).\n")
	} else {
		status := func(ctx context.Context) ([]byte, error) {
			return env.httpGet()(ctx, "http://127.0.0.1:"+strconv.Itoa(g.s.HTTPPort)+"/status")
		}
		if err := g.enroll(ctx, status); err != nil {
			return err
		}
	}
	say("Then run `%s doctor`.\n", env.Deps.command())
	sayOldEnvFile(out, filepath.Join(configDir, "github.env"), oldEnvFile, leftRunning)
	scr.Step(7)
	return showSummary(scr, func() []ui.KV { return isolatedSummary(ctx, env, host, g.s, ui.KV{Key: "Owner", Value: g.owner}) })
}

// checkEnvFileHoldsNoApp refuses, before any change, a run without an App over an env file that holds one. The service reads the
// file whatever the run says, and without the App key lines it would stop at startup.
func (g *githubInstall) checkEnvFileHoldsNoApp(envPath string) error {
	if g.s.GitHub.AppID != 0 {
		return nil
	}
	held, err := readGitHubEnvFile(envPath)
	if err != nil {
		return err
	}
	if held.AppID != 0 {
		return fmt.Errorf("GitHub is off in this run, but %s still holds the App %d and the service reads that file: remove its three SUITEWARD_GITHUB_ lines (or the file) first, or run again without --github-app-id none", envPath, held.AppID)
	}
	return nil
}

// checkAppKey refuses, before any change, an install with an App and no App key to store: the service unit of an App mounts that
// secret, so without it the unit does not start. Without an App there is no key to check. It prints the key's fingerprint when
// this run stores a key it was given. The way out names the env file when it holds an App: the service reads that file, and
// --github-app-id none alone is refused while it does.
func (g *githubInstall) checkAppKey(ctx context.Context, envPath string) error {
	if g.githubKey != nil {
		fingerprint, err := appKeyFingerprint(g.githubKey)
		if err != nil {
			return err
		}
		g.githubKey = append(bytes.TrimSpace(g.githubKey), '\n') // one trailing newline, whatever the file or stdin had
		screenOn(g.out).Keep("App private key fingerprint: %s\nCompare it with the fingerprint of the key on the App's settings page; a wrong or revoked key looks right to every other check.\n", fingerprint)
		return nil
	}
	if g.s.GitHub.AppID == 0 {
		return nil
	}
	have, err := g.rt.Secrets().Exists(ctx, SecretGitHubAppKey)
	if err != nil {
		return err
	}
	if !have {
		withoutGitHub := "pass --github-app-id none to run without GitHub"
		held, err := readGitHubEnvFile(envPath)
		if err != nil {
			return err
		}
		if held.AppID != 0 {
			withoutGitHub = "remove the three SUITEWARD_GITHUB_ lines from " + envPath + " (the service reads that file) and pass --github-app-id none to run without GitHub"
		}
		return fmt.Errorf("the service mounts the secret %s for the App %d and it does not exist: pass --github-app-key-file with the App's private key, or %s", SecretGitHubAppKey, g.s.GitHub.AppID, withoutGitHub)
	}
	return nil
}

// sayOldEnvFile tells the administrator when the old /etc env file may go: only once the service runs on the new one.
// A service that was already running is not restarted, so it reads the new file only at its next start.
func sayOldEnvFile(out io.Writer, current, old string, leftRunning bool) {
	if old == "" {
		return
	}
	reads := "now reads its GitHub settings from " + current
	if leftRunning {
		reads = "will read its GitHub settings from " + current + " on its next start (it was already running and was not restarted)"
	}
	screenOn(out).Keep("\nThe service %s. An administrator may remove the old file: sudo rm %s\n", reads, old)
}

// ensureNetwork restarts the network service of the stack when the database, the migration and the service (serviceActive, which
// the caller knows) are all not active. That service is a oneshot that stays active once it ran: after a purge removed the Podman
// network, a database started while the service is still active exits 125 (network not found), because systemd never runs
// `podman network create` again. With nothing of the stack running nothing needs the network, so the restart is safe, and it
// creates the network again (the unit runs it with --ignore, so an existing network is no problem). Every container requires the
// network service, so a restart while any of them runs would restart it: a database that crashed while the migration or the
// service runs is no reason, and a stack that runs keeps the network it has.
func ensureNetwork(ctx context.Context, systemctl func(ctx context.Context, args ...string) (string, error), serviceActive bool) error {
	if serviceActive {
		return nil
	}
	for _, unit := range []string{"suiteward-db.service", "suiteward-migrate.service"} {
		state, err := systemctl(ctx, "show", "--property=ActiveState", "--value", unit)
		if err != nil {
			return fmt.Errorf("ask for the state of %s: %w", unit, err)
		}
		if state == "active" {
			return nil
		}
	}
	network := unitService("suiteward.network")
	if _, err := systemctl(ctx, "restart", network); err != nil {
		return fmt.Errorf("restart the network (read `journalctl --user -u %s -n 50 --no-pager`): %w", network, err)
	}
	return nil
}

// startStack starts the database (after the network service, when nothing of the stack runs), waits for it, creates the application
// role, starts the service (the unit dependency runs migrate first) and checks the three units. It reports whether a running
// service was left as it was: starting a running service does nothing, so it keeps what it read at its last start. With restart
// (what it reads changed: its unit, its env file or its key secret) a running service is restarted instead, so that it takes them.
func startStack(ctx context.Context, rt Runtime, d Deps, scr *ui.Screen, restart bool) (leftRunning bool, err error) {
	systemctl := userSystemctl(d)
	before, err := systemctl(ctx, "show", "--property=ActiveState", "--value", "suiteward.service")
	if err != nil {
		return false, fmt.Errorf("ask for the state of suiteward.service: %w", err)
	}
	running := before == "active"
	leftRunning = running && !restart
	if err := ensureNetwork(ctx, systemctl, running); err != nil {
		return leftRunning, err
	}
	if _, err := systemctl(ctx, "start", "suiteward-db.service"); err != nil {
		return leftRunning, fmt.Errorf("start the database: %w", err)
	}
	if err := waitHealthy(ctx, rt, barOn(scr, "Database")); err != nil {
		return leftRunning, err
	}
	if err := ensureAppRole(ctx, rt); err != nil {
		return leftRunning, err
	}
	if running { // a running service keeps what it started with: starting it again does nothing
		verb := "start"
		if restart {
			verb = "restart"
		}
		if _, err := systemctl(ctx, verb, "suiteward.service"); err != nil {
			return leftRunning, fmt.Errorf("%s the service: %w", verb, err)
		}
		return leftRunning, checkUnitsActive(ctx, systemctl)
	}
	// The migration unit stays active after it ran (RemainAfterExit), so starting the service would not run it again for a release
	// that changed since: a stopped service gets the migration restarted first.
	return false, migrateAndStart(ctx, d, scr)
}

// userSystemctl runs `systemctl --user` and returns its trimmed output.
func userSystemctl(d Deps) func(ctx context.Context, args ...string) (string, error) {
	return func(ctx context.Context, args ...string) (string, error) {
		b, err := d.Exec(ctx, "systemctl", append([]string{"--user"}, args...), nil)
		return strings.TrimSpace(string(b)), err
	}
}

// checkUnitsActive checks the three units after a start.
func checkUnitsActive(ctx context.Context, systemctl func(ctx context.Context, args ...string) (string, error)) error {
	for _, unit := range []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service"} {
		if state, err := systemctl(ctx, "show", "--property=ActiveState", "--value", unit); err != nil || state != "active" {
			return fmt.Errorf("%s is %q after the start, want active: read `journalctl --user -u %s -n 50 --no-pager`", unit, state, unit)
		}
	}
	return nil
}

// The wait for a service that has just started to prove itself: stablePolls answers of /readyz in a row with no restart of the
// unit meanwhile, at most stableTries polls stableEvery apart (the injected Env.Sleep, so tests do not sleep). A unit that
// Restart=always keeps restarting is "active" at the moment a check looks, so the units' state alone proves nothing.
// The polls span more than the RestartSec=5 of the unit, so a service that dies right after it answers has failed a poll or
// restarted before the wait ends; one that lives longer than that is not caught.
const (
	stablePolls = 4
	stableTries = 45
	stableEvery = 2 * time.Second
)

// waitStable returns nil once /readyz of the service on httpPort answered 200 stablePolls times in a row and the restart count
// of suiteward.service did not grow since before the first poll. Otherwise it names the journal to read.
func waitStable(ctx context.Context, env Env, scr *ui.Screen, httpPort int) error {
	bar := barOn(scr, "Service")
	address := "http://127.0.0.1:" + strconv.Itoa(httpPort) + "/readyz"
	systemctl := userSystemctl(env.Deps)
	restarts := func() (int, error) {
		text, err := systemctl(ctx, "show", "--property=NRestarts", "--value", "suiteward.service")
		if err != nil {
			return 0, fmt.Errorf("ask for the restarts of suiteward.service: %w", err)
		}
		n, err := strconv.Atoi(text)
		if err != nil {
			return 0, fmt.Errorf("the restart count of suiteward.service (%q) is not a number", text)
		}
		return n, nil
	}
	const journal = "read `journalctl --user -u suiteward.service -n 50 --no-pager`"
	first, err := restarts()
	if err != nil {
		return err
	}
	answered := 0
	var lastErr error
	for i := range stableTries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := env.httpGet()(ctx, address); err == nil {
			answered++
		} else {
			answered, lastErr = 0, err
		}
		n, err := restarts()
		if err != nil {
			return err
		}
		if n > first {
			return fmt.Errorf("suiteward.service restarted %d times while waiting for it to settle: %s", n-first, journal)
		}
		bar.set(float64(answered)/stablePolls, fmt.Sprintf("answered %d of %d times", answered, stablePolls))
		if answered == stablePolls {
			return nil
		}
		if i < stableTries-1 {
			env.Sleep(stableEvery)
		}
	}
	return fmt.Errorf("%s did not answer 200 %d times in a row within %d polls (last answer: %v): %s", address, stablePolls, stableTries, lastErr, journal)
}

// checkAccount refuses a run by root, by another user than the service account, or with a HOME that is not the account's
// home directory in the passwd database: the installer writes under HOME and must not be steered by the environment. It
// returns HOME.
func checkAccount(run func(string, ...string) (string, error), home string) (string, error) {
	command := "sudo -u " + ServiceAccount + " -H env XDG_RUNTIME_DIR=/run/user/<uid of " + ServiceAccount + "> DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus suiteward install (`sudo suiteward install host` prints it with the uid filled in)"
	if uid, err := run("id", "-u"); err != nil {
		return "", fmt.Errorf("find out who is running the installer: %w", err)
	} else if uid == "0" {
		return "", fmt.Errorf("run the installer as the service account, not as root: %s", command)
	}
	user, err := run("id", "-un")
	if err != nil {
		return "", fmt.Errorf("find the account name: %w", err)
	}
	if user != ServiceAccount {
		return "", fmt.Errorf("the installer is running as %q, not as %s: %s", user, ServiceAccount, command)
	}
	entry, err := run("getent", "passwd", ServiceAccount)
	if err != nil {
		return "", fmt.Errorf("look up the account %s: %w", ServiceAccount, err)
	}
	fields := strings.Split(entry, ":")
	if len(fields) < 7 {
		return "", fmt.Errorf("the account entry %q is not a passwd line", entry)
	}
	passwdHome := strings.Join(fields[5:len(fields)-1], ":") // the home is field 6; a colon inside it must not split it
	if !filepath.IsAbs(home) || filepath.Clean(home) != filepath.Clean(passwdHome) {
		return "", fmt.Errorf("HOME is %q but the home of %s is %q: %s", home, ServiceAccount, passwdHome, command)
	}
	return home, nil
}

// stackNeverStarted says whether this stack never completed a start: no successful run saved the settings and the service was
// never active. Only then may a retry pin another image; otherwise changing the image is an upgrade.
func stackNeverStarted(run func(string, ...string) (string, error), settingsPath string) (bool, error) {
	if _, err := os.Stat(settingsPath); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("look for %s: %w", settingsPath, err)
	}
	entered, err := run("systemctl", "--user", "show", "--property=ActiveEnterTimestamp", "--value", "suiteward.service")
	if err != nil {
		return false, fmt.Errorf("ask when suiteward.service was last active: %w", err)
	}
	return entered == "", nil
}

// checkUserManager checks the account's systemd user manager and linger (docs/install.md, section 10). `show` rather than
// is-system-running: that command exits non-zero for a degraded manager, which the guide accepts, and Exec drops stdout then.
func checkUserManager(run func(string, ...string) (string, error)) error {
	state, err := run("systemctl", "--user", "show", "--property=SystemState", "--value")
	if err != nil {
		return fmt.Errorf("ask the systemd user manager for its state (open the shell as `sudo suiteward install host` prints it, with XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS set): %w", err)
	}
	if state != "running" && state != "degraded" {
		return fmt.Errorf("the systemd user manager is %q, want running or degraded: wait a few seconds and run again", state)
	}
	user, err := run("id", "-un")
	if err != nil {
		return fmt.Errorf("find the account name: %w", err)
	}
	linger, err := run("loginctl", "show-user", user, "--property=Linger", "--value")
	if err != nil || linger != "yes" {
		return fmt.Errorf("linger is not enabled for %s, so its services would stop at logout: an administrator runs `sudo suiteward install host`", user)
	}
	return nil
}

// adoptInstalled compares units installed earlier (by hand from the install guide, or by this installer) with the units of
// this release, carries their tier, ports and job timeout into s and returns the image digest to pin. A difference it cannot
// carry stops the run with the diff. Without installed units the digest comes from the "image" answer.
// The second result is what the unit directory holds: whether anything is installed, and the env file the installed units read
// when it is not the account's own, so the caller can say when it may go. The installed tier replaces the one that was asked for, and
// that is reported; the installed ports and job timeout stand unless the run is given others (a flag or the answers file), which win,
// and the change is said.
func adoptInstalled(cmd string, s *Settings, unitsDir, homeEnvFile string, r *Resolver, neverStarted func() (bool, error), say func(string, ...any)) (string, installedStack, error) {
	st, err := adoptUnitsDir(unitsDir, homeEnvFile, false)
	if err != nil {
		return "", installedStack{}, err
	}
	installedDigest := ""
	if st.found {
		kept := installedWithGiven(r, st.adopted.Settings, *s)
		reportAdopted(say, *s, kept)
		sayChanges(say, st.adopted.Settings, kept)
		s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = kept.Tier, kept.HTTPPort, kept.MCPPort, kept.JobTimeout
		installedDigest = strings.TrimPrefix(st.adopted.Image, releaseImageRepo+"@")
	}
	digest, err := chooseImage(cmd, r, installedDigest, neverStarted, say)
	return digest, st, err
}

// installedWithGiven is the installed settings with the ports and the job timeout that the run is given over them: on a re-run
// those win, and everything else that is installed stays. asked holds the resolved values (askedSettings).
func installedWithGiven(r *Resolver, installed, asked Settings) Settings {
	s := installed
	if _, ok := r.given("http-port"); ok {
		s.HTTPPort = asked.HTTPPort
	}
	if _, ok := r.given("mcp-port"); ok {
		s.MCPPort = asked.MCPPort
	}
	if _, ok := r.given("job-timeout"); ok {
		s.JobTimeout = asked.JobTimeout
	}
	return s
}

// sayChanges says which installed port or job timeout this run changes.
func sayChanges(say func(string, ...any), installed, now Settings) {
	if now.HTTPPort != installed.HTTPPort {
		say("Changing the HTTP port of the installed stack from %d to %d, as asked.\n", installed.HTTPPort, now.HTTPPort)
	}
	if now.MCPPort != installed.MCPPort {
		say("Changing the MCP port of the installed stack from %d to %d, as asked.\n", installed.MCPPort, now.MCPPort)
	}
	if now.JobTimeout != installed.JobTimeout {
		say("Changing the job timeout of the installed stack from %q to %q, as asked.\n", installed.JobTimeout, now.JobTimeout)
	}
}

// mustRestart says whether a service that runs has to be restarted to read what this run changed: its unit (the ports, the job
// timeout, the GitHub key lines, the image; the path of the env file is left out, because moving the file does not change its
// values), the values in its env file (envChanged), or its key secret (keyStored). Without an installed stack nothing runs.
func (st installedStack) mustRestart(units map[string][]byte, envChanged, keyStored bool) bool {
	return st.found && (envChanged || keyStored || len(diffUnitLines(serveUnit, units[serveUnit], st.units[serveUnit], "EnvironmentFile=")) > 0)
}

// ownedPorts are the ports whose listener is the stack's own service: the ones its units publish, while the service is active.
func ownedPorts(ctx context.Context, d Deps, st installedStack) ([]int, error) {
	if !st.found {
		return nil, nil
	}
	state, err := userSystemctl(d)(ctx, "show", "--property=ActiveState", "--value", "suiteward.service")
	if err != nil {
		return nil, fmt.Errorf("ask for the state of suiteward.service: %w", err)
	}
	if state != "active" {
		return nil, nil
	}
	return []int{st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort}, nil
}

// installedStack is what an account's unit directory holds.
type installedStack struct {
	found   bool // at least one container unit is installed
	adopted Adopted
	oldEnv  string            // the env file the installed units read when it is not the account's own
	units   map[string][]byte // the installed unit files by name
}

// adoptUnitsDir reads the installed units and compares them with the units of this release. A difference it cannot carry is a
// *DiffError. With refreshDB (an upgrade) a database unit on another PostgreSQL pin is not a difference. When the installed
// units read an env file that is not the account's own, that file must already be copied to homeEnvFile.
func adoptUnitsDir(unitsDir, homeEnvFile string, refreshDB bool) (installedStack, error) {
	templates, err := embeddedUnits()
	if err != nil {
		return installedStack{}, err
	}
	st := installedStack{units: map[string][]byte{}}
	for name := range templates {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(unitsDir, name)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return installedStack{}, fmt.Errorf("read the installed unit %s: %w", name, err)
		}
		st.units[name] = raw
		st.found = st.found || strings.HasSuffix(name, ".container")
	}
	if !st.found {
		return st, nil
	}
	if st.adopted, err = adoptUnits(st.units, nil, refreshDB); err != nil {
		return installedStack{}, err
	}
	if st.adopted.EnvFile != DefaultEnvFile {
		st.oldEnv = st.adopted.EnvFile
		if _, err := os.Stat(homeEnvFile); err != nil {
			return installedStack{}, fmt.Errorf("the installed units read the GitHub env file at %s; an administrator runs `sudo suiteward install host`, which copies it to %s, then run this again", st.adopted.EnvFile, homeEnvFile)
		}
	}
	return st, nil
}

// reportAdopted says which installed tier replaces the one that was asked for. A port or the job timeout is not reported: it differs
// from the installed one only when the run was not given it (installedWithGiven), so nothing was asked for, and one that is given wins
// and is said by sayChanges.
func reportAdopted(say func(string, ...any), asked, installed Settings) {
	if asked.Tier != installed.Tier {
		say("Keeping the installed tier %s (you asked for %s).\n", installed.Tier, asked.Tier)
	}
}

// chooseImage picks the digest to pin from the "image" answer and the installed one. A different image is an upgrade, which
// has its own command, unless the stack never completed a start: then it is a retry after a failed first run.
func chooseImage(cmd string, r *Resolver, installedDigest string, neverStarted func() (bool, error), say func(string, ...any)) (string, error) {
	asked, _ := r.given("image")
	switch {
	case asked == "" && installedDigest == "":
		if embedded, ok := ReleaseImage(); ok {
			return strings.TrimPrefix(embedded, releaseImageRepo+"@"), nil
		}
		return "", errors.New("this binary carries no release image (a development build), so --image is required: pass --image sha256:<signed index digest> (the release notes name it; verify it first as the install guide says)")
	case asked == "":
		return installedDigest, nil
	}
	image, err := parseImage(asked)
	if err != nil {
		return "", err
	}
	repo, digest, _ := strings.Cut(image, "@") // parseImage made sure there is a digest
	if withoutTag(repo) != releaseImageRepo {
		return "", fmt.Errorf("%w: the isolated tier runs %s only, not %s", ErrNotDigestImage, releaseImageRepo, image)
	}
	if installedDigest != "" && digest != installedDigest {
		never, err := neverStarted()
		if err != nil {
			return "", err
		}
		if !never {
			return "", fmt.Errorf("--image names %s but %s is installed: changing the release is an upgrade, which backs up first; use `suiteward install --upgrade`", digest, installedDigest)
		}
		say("Replacing the installed image %s with %s: this stack never completed a start, so this is a retry, not an upgrade.\n", installedDigest, digest)
	}
	return digest, nil
}

func appKeyFingerprint(raw []byte) (string, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("the App key is not a PEM private key")
	}
	var parsed any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return "", fmt.Errorf("the PEM block is a %q, not a private key", block.Type)
	}
	if err != nil {
		return "", fmt.Errorf("the App key does not parse: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("the App key is not an RSA key")
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", fmt.Errorf("encode the public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// ensureSecrets creates the secrets that are missing (the application role's one comes later).
// The database password is never regenerated once it exists: the database keeps it in its volume. Secrets that exist are
// not shape-checked again: the ones this installer generated are right by construction, and one made by hand is the administrator's to have checked. appKey is nil when the secret exists
// already.
func ensureSecrets(ctx context.Context, store SecretStore, appKey []byte) error {
	havePassword, err := store.Exists(ctx, SecretDBAuth)
	if err != nil {
		return err
	}
	haveOwnerURL, err := store.Exists(ctx, SecretDatabaseOwnerURL)
	if err != nil {
		return err
	}
	var password string
	switch {
	case havePassword && haveOwnerURL:
	case haveOwnerURL:
		return fmt.Errorf("the secret %s exists but %s does not: the password cannot be recovered from the URL safely; if the database never started, remove %s and run again", SecretDatabaseOwnerURL, SecretDBAuth, SecretDatabaseOwnerURL)
	case havePassword:
		stored, err := store.Read(ctx, SecretDBAuth)
		if err != nil {
			return err
		}
		password = string(stored)
	default:
		if password, err = NewPassword(); err != nil {
			return err
		}
		if err := store.Write(ctx, SecretDBAuth, []byte(password), false); err != nil {
			return err
		}
	}
	if !haveOwnerURL {
		if err := store.Write(ctx, SecretDatabaseOwnerURL, []byte(DatabaseURL("suiteward", password)), false); err != nil {
			return err
		}
	}
	if have, err := store.Exists(ctx, SecretMCPAuth); err != nil {
		return err
	} else if !have {
		token, err := NewMCPToken()
		if err != nil {
			return err
		}
		if err := store.Write(ctx, SecretMCPAuth, []byte(token), false); err != nil {
			return err
		}
	}
	if appKey != nil {
		return store.Write(ctx, SecretGitHubAppKey, appKey, false)
	}
	return nil
}

// writeEnvFile writes the GitHub env file the service unit loads: the three values, or an empty file because the unit's
// EnvironmentFile= makes a missing file fail the start. A file that exists is left alone unless there are new values. It
// reports whether the values the service reads from the file changed: a file that did not exist and is made empty did not.
func writeEnvFile(path string, gh GitHub, have bool) (changed bool, err error) {
	existing, err := os.ReadFile(filepath.Clean(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	exists := err == nil
	var content []byte
	switch {
	case have:
		content = fmt.Appendf(nil, "SUITEWARD_GITHUB_APP_ID=%d\nSUITEWARD_GITHUB_INSTALLATION_ID=%d\nSUITEWARD_GITHUB_REPOSITORIES=%s\n", gh.AppID, gh.InstallationID, strings.Join(gh.Repositories, ","))
		if exists && bytes.Equal(existing, content) {
			return false, nil
		}
	case exists:
		return false, nil
	}
	return !bytes.Equal(existing, content), writeFileAtomic(path, content, 0o700)
}

// dryRun runs the Podman generator's dry run and checks that it listed the service of every unit: Podman
// skips a unit it cannot parse, and the loss would only show later as "unit not found".
func dryRun(ctx context.Context, d Deps, units map[string][]byte) error {
	out, err := d.Exec(ctx, generatorPath, []string{"--user", "--dryrun"}, nil)
	if err != nil {
		return fmt.Errorf("the Podman generator rejects the units (if it is not at %s, find it with `rpm -ql podman | grep generator`): %w", generatorPath, err)
	}
	// Each generated unit starts with a ---name--- header line (confirmed on Podman 6.1.4 with the repo units: the six are
	// ---suiteward-data-volume.service---, ---suiteward-db-data-volume.service---, ---suiteward-network.service---,
	// ---suiteward-db.service---, ---suiteward-migrate.service--- and ---suiteward.service---; volume and network units get a
	// -volume or -network suffix). A header,
	// not the bare name: the units name each other in Requires= and After= lines, so a skipped unit's name still appears.
	listed := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); len(line) > 6 && strings.HasPrefix(line, "---") && strings.HasSuffix(line, "---") {
			listed[line[3:len(line)-3]] = true
		}
	}
	for _, file := range sortedKeys(units) {
		service := unitService(file)
		if !listed[service] {
			return fmt.Errorf("the Podman generator did not list %s for %s: it could not read that unit; run `%s --user --dryrun` and read its error", service, file, generatorPath)
		}
	}
	return nil
}

// unitService is the service the Podman generator makes of a unit file: suiteward.container is suiteward.service, a volume or a
// network gets its kind as a suffix.
func unitService(file string) string {
	name, kind, _ := strings.Cut(file, ".")
	if kind != "container" {
		return name + "-" + kind + ".service"
	}
	return name + ".service"
}

// waitHealthy waits for the database container to report healthy; the guide's loop has no limit, this one has.
func waitHealthy(ctx context.Context, rt Runtime, bar waitBar) error {
	start := time.Now()
	deadline := start.Add(dbHealthTimeout)
	for {
		state, err := rt.State(ctx, ContainerDB)
		last := state.Health
		if err != nil {
			last = err.Error()
		}
		if err == nil && state.Health == "healthy" {
			bar.set(1, "healthy")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the database was not healthy within %s (last: %q): read `journalctl --user -u suiteward-db.service -n 50 --no-pager`", dbHealthTimeout, last)
		}
		bar.set(float64(time.Since(start))/float64(dbHealthTimeout), "starting")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dbHealthInterval):
		}
	}
}

// ensureAppRole creates the application role and its URL secret once. A role left by an earlier attempt gets the new password
// instead.
func ensureAppRole(ctx context.Context, rt Runtime) error {
	store := rt.Secrets()
	if have, err := store.Exists(ctx, SecretDatabaseURL); err != nil || have {
		return err
	}
	exists, err := appRoleExists(ctx, rt)
	if err != nil {
		return fmt.Errorf("look for the role %s: %w", appRole, err)
	}
	password, err := setAppRolePassword(ctx, rt, exists)
	if err != nil {
		return fmt.Errorf("create the role %s: %w", appRole, err)
	}
	return store.Write(ctx, SecretDatabaseURL, []byte(DatabaseURL(appRole, password)), false)
}

// The names the service reads its GitHub values by; the installer writes them to the env file (writeEnvFile).
const (
	envAppID          = "SUITEWARD_GITHUB_APP_ID"
	envInstallationID = "SUITEWARD_GITHUB_INSTALLATION_ID"
	envRepositories   = "SUITEWARD_GITHUB_REPOSITORIES"
)

// readGitHubEnvFile reads the GitHub values of an env file: the App of a host installed by hand, or of a run that stopped after
// it wrote the file. A missing or empty file holds none. Anything else must be exactly the three keys once each, with a positive
// App id and installation id and 1 to MaxRepositories owner/name of one owner; a file that is not that is an error, which names
// the file, the line or the key and never a value.
func readGitHubEnvFile(path string) (GitHub, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return GitHub{}, nil
	}
	if err != nil {
		return GitHub{}, fmt.Errorf("read %s: %w", path, err)
	}
	values := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		switch {
		case !ok:
			return GitHub{}, fmt.Errorf("%s line %d is not KEY=value", path, i+1)
		case key != envAppID && key != envInstallationID && key != envRepositories:
			return GitHub{}, fmt.Errorf("%s line %d is not one of %s, %s and %s", path, i+1, envAppID, envInstallationID, envRepositories)
		}
		if _, repeated := values[key]; repeated {
			return GitHub{}, fmt.Errorf("%s line %d repeats %s", path, i+1, key)
		}
		values[key] = value
	}
	if len(values) == 0 {
		return GitHub{}, nil
	}
	for _, key := range []string{envAppID, envInstallationID, envRepositories} {
		if _, ok := values[key]; !ok {
			return GitHub{}, fmt.Errorf("%s has some of the three GitHub values but not %s: the service stops at startup when only some are set; complete the file or remove it", path, key)
		}
	}
	appID, err := strconv.ParseInt(values[envAppID], 10, 64)
	if err != nil || appID < 1 {
		return GitHub{}, fmt.Errorf("%s: %s is not a positive number", path, envAppID)
	}
	installationID, err := strconv.ParseInt(values[envInstallationID], 10, 64)
	if err != nil || installationID < 1 {
		return GitHub{}, fmt.Errorf("%s: %s is not a positive number", path, envInstallationID)
	}
	repos := strings.Split(values[envRepositories], ",")
	owner, _, _ := strings.Cut(repos[0], "/")
	gh := GitHub{AppID: appID, InstallationID: installationID, Owner: owner, Repositories: repos}
	if gh.validate() != nil { // its message repeats the values
		return GitHub{}, fmt.Errorf("%s: %s is not 1 to %d owner/name pairs of one owner, separated by commas without spaces", path, envRepositories, MaxRepositories)
	}
	return gh, nil
}
