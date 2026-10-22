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
	"strconv"
	"strings"
	"time"
)

// The service-account half of the isolated install, run as the account `sudo suiteward install host` prepared and never as
// root. installIsolated(ctx, s, r, env) takes the parsed settings, the Resolver (flags, answers file, prompts), the
// machine's Env (its Deps and the writer for its report). It reads these answers: "image" (sha256:<index digest> or the full
// reference; optional when an install is adopted), "github-app-key" (the PEM, from the answers file or stdin, never a flag),
// "github-app-key-file" (a path to the PEM) and, all three or none, "github-app-id", "github-installation-id" and
// "github-repositories" (comma separated). Every external command goes through Deps.Exec.
//
// The order is fixed: checks and the adoption guard first, so a refusal changes nothing; then every input is resolved and
// checked; then secrets, the GitHub env file, images, units, the generator dry run, the database, the application role, the
// service, and last the settings file.

// Health wait of the database, variables so tests do not sleep.
var (
	dbHealthTimeout  = 90 * time.Second
	dbHealthInterval = 2 * time.Second
)

// generatorPath is the Podman quadlet generator, whose dry run lists every unit it reads.
const generatorPath = "/usr/lib/systemd/system-generators/podman-system-generator"

const appDatabaseRole = "suiteward_app"

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

func installIsolated(ctx context.Context, s Settings, r *Resolver, env Env) error {
	d, out := env.Deps, env.Term.Out
	host, err := openIsolatedHost(ctx, s, d)
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	rt, configDir, unitsDir, settingsPath := host.rt, host.configDir, host.unitsDir, host.settingsPath
	run := host.run

	// The adoption guard and the image, before anything is changed.
	neverStarted := func() (bool, error) { return stackNeverStarted(run, settingsPath) }
	digest, oldEnvFile, err := adoptInstalled(&s, unitsDir, filepath.Join(configDir, "github.env"), r, neverStarted, say)
	if err != nil {
		return err
	}

	// Every input, resolved and checked.
	gh, haveGH, err := githubInputs(r, s.GitHub)
	if err != nil {
		return err
	}
	store := rt.Secrets()
	appKey, err := appKeyIfMissing(ctx, store, r, out)
	if err != nil {
		return err
	}

	// Changes begin here.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", configDir, err)
	}
	if err := ensureSecrets(ctx, store, appKey); err != nil {
		return err
	}
	if err := writeEnvFile(filepath.Join(configDir, "github.env"), gh, haveGH); err != nil {
		return err
	}
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	for _, image := range []string{postgres, releaseImageRepo + "@" + digest} {
		if err := rt.Pull(ctx, image); err != nil {
			return err
		}
	}
	units, err := RenderUnits(UnitParamsFrom(s, digest))
	if err != nil {
		return err
	}
	if _, err := WriteUnits(unitsDir, units); err != nil {
		return err
	}
	if err := dryRun(ctx, d, units); err != nil {
		return err
	}
	if _, err := d.Exec(ctx, DaemonReload[0], DaemonReload[1:], nil); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}

	wasRunning, err := startStack(ctx, rt, d)
	if err != nil {
		return err
	}

	if haveGH {
		s.GitHub = gh
	}
	if err := SaveSettings(settingsPath, s); err != nil {
		return err
	}
	say("\nSuiteWard %s is running on this account. Settings are saved in %s.\n\n", digest, settingsPath)
	say("Next, turn off recording of this terminal session (setup prints the TOTP secret), then enroll yourself as the owner:\n\n")
	say("  podman exec -it suiteward /suiteward setup --owner <login> --test-check '<owner/name>=<check name>'\n\n")
	say("Then run `suiteward doctor`.\n")
	sayOldEnvFile(out, filepath.Join(configDir, "github.env"), oldEnvFile, wasRunning)
	return nil
}

// sayOldEnvFile tells the administrator when the old /etc env file may go: only once the service runs on the new one.
// A service that was already running is not restarted, so it reads the new file only at its next start.
func sayOldEnvFile(out io.Writer, current, old string, wasRunning bool) {
	if old == "" {
		return
	}
	reads := "now reads its GitHub settings from " + current
	if wasRunning {
		reads = "will read its GitHub settings from " + current + " on its next start (it was already running and was not restarted)"
	}
	_, _ = fmt.Fprintf(out, "\nThe service %s. An administrator may remove the old file: sudo rm %s\n", reads, old)
}

// appKeyIfMissing reads the App key unless its secret exists, and reports the fingerprint to compare with GitHub's page.
func appKeyIfMissing(ctx context.Context, store SecretStore, r *Resolver, out io.Writer) ([]byte, error) {
	have, err := store.Exists(ctx, SecretGitHubAppKey)
	if err != nil || have {
		return nil, err
	}
	key, fingerprint, err := readIsolatedAppKey(r)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(out, "App private key fingerprint: %s\nCompare it with the fingerprint of the key on the App's settings page; a wrong or revoked key looks right to every other check.\n", fingerprint)
	return key, nil
}

// startStack starts the database, waits for it, creates the application role, starts the service (the unit dependency runs
// migrate first) and checks the three units (install.md 10.11 to 10.13). It reports whether the service was already running:
// starting a running service does nothing, so it keeps what it read at its last start.
func startStack(ctx context.Context, rt Runtime, d Deps) (wasRunning bool, err error) {
	systemctl := userSystemctl(d)
	before, err := systemctl(ctx, "show", "--property=ActiveState", "--value", "suiteward.service")
	if err != nil {
		return false, fmt.Errorf("ask for the state of suiteward.service: %w", err)
	}
	wasRunning = before == "active"
	if _, err := systemctl(ctx, "start", "suiteward-db.service"); err != nil {
		return wasRunning, fmt.Errorf("start the database: %w", err)
	}
	if err := waitHealthy(ctx, rt); err != nil {
		return wasRunning, err
	}
	if err := ensureAppRole(ctx, rt); err != nil {
		return wasRunning, err
	}
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := systemctl(startCtx, "start", "suiteward.service"); err != nil {
		return wasRunning, fmt.Errorf("start the service (read the migration with `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`): %w", err)
	}
	return wasRunning, checkUnitsActive(ctx, systemctl)
}

// userSystemctl runs `systemctl --user` and returns its trimmed output.
func userSystemctl(d Deps) func(ctx context.Context, args ...string) (string, error) {
	return func(ctx context.Context, args ...string) (string, error) {
		b, err := d.Exec(ctx, "systemctl", append([]string{"--user"}, args...), nil)
		return strings.TrimSpace(string(b)), err
	}
}

// checkUnitsActive checks the three units after a start (install.md 10.13).
func checkUnitsActive(ctx context.Context, systemctl func(ctx context.Context, args ...string) (string, error)) error {
	for _, unit := range []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service"} {
		if state, err := systemctl(ctx, "show", "--property=ActiveState", "--value", unit); err != nil || state != "active" {
			return fmt.Errorf("%s is %q after the start, want active: read `journalctl --user -u %s -n 50 --no-pager`", unit, state, unit)
		}
	}
	return nil
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

// checkUserManager checks the account's systemd user manager and linger (install.md 10.1 and 10.2). `show` rather than
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
// The second result is the env file the installed units read when it is not the account's own, so the caller can say when it
// may go. What is installed replaces what was asked for, and each such replacement is reported.
func adoptInstalled(s *Settings, unitsDir, homeEnvFile string, r *Resolver, neverStarted func() (bool, error), say func(string, ...any)) (string, string, error) {
	st, err := adoptUnitsDir(unitsDir, homeEnvFile, false)
	if err != nil {
		return "", "", err
	}
	installedDigest := ""
	if st.found {
		reportAdopted(say, *s, st.adopted.Settings)
		s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = st.adopted.Settings.Tier, st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort, st.adopted.Settings.JobTimeout
		installedDigest = strings.TrimPrefix(st.adopted.Image, releaseImageRepo+"@")
	}
	digest, err := chooseImage(r, installedDigest, neverStarted, say)
	return digest, st.oldEnv, err
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

// reportAdopted says which installed values replace the ones that were asked for.
func reportAdopted(say func(string, ...any), asked, installed Settings) {
	if asked.Tier != installed.Tier {
		say("Keeping the installed tier %s (you asked for %s).\n", installed.Tier, asked.Tier)
	}
	if asked.HTTPPort != installed.HTTPPort {
		say("Keeping the installed HTTP port %d (you asked for %d).\n", installed.HTTPPort, asked.HTTPPort)
	}
	if asked.MCPPort != installed.MCPPort {
		say("Keeping the installed MCP port %d (you asked for %d).\n", installed.MCPPort, asked.MCPPort)
	}
	if asked.JobTimeout != installed.JobTimeout {
		say("Keeping the installed job timeout %q (you asked for %q).\n", installed.JobTimeout, asked.JobTimeout)
	}
}

// chooseImage picks the digest to pin from the "image" answer and the installed one. A different image is an upgrade, which
// has its own command, unless the stack never completed a start: then it is a retry after a failed first run.
func chooseImage(r *Resolver, installedDigest string, neverStarted func() (bool, error), say func(string, ...any)) (string, error) {
	asked, _ := r.optional("image")
	asked = strings.TrimPrefix(asked, releaseImageRepo+"@")
	switch {
	case asked == "" && installedDigest == "":
		return "", errors.New("the release image is required: pass --image sha256:<signed index digest> (the release notes name it; verify it first as the install guide says)")
	case asked == "":
		return installedDigest, nil
	case !imageDigest.MatchString(asked):
		return "", fmt.Errorf("--image %q is not a digest of the form sha256:<64 lowercase hex characters>: the installer never pins a tag", asked)
	case installedDigest != "" && asked != installedDigest:
		never, err := neverStarted()
		if err != nil {
			return "", err
		}
		if !never {
			return "", fmt.Errorf("--image names %s but %s is installed: changing the release is an upgrade, which backs up first; use `suiteward install --upgrade`", asked, installedDigest)
		}
		say("Replacing the installed image %s with %s: this stack never completed a start, so this is a retry, not an upgrade.\n", installedDigest, asked)
	}
	return asked, nil
}

// optional returns the flag or answers-file value of key, when there is one: it never prompts.
func (r *Resolver) optional(key string) (string, bool) {
	if v, ok := r.flags[key]; ok {
		return v, true
	}
	v, ok := r.answers[key]
	return v, ok
}

// githubInputs reads the three non-secret GitHub values: all of them or none. The settings win when they hold the values.
func githubInputs(r *Resolver, current GitHub) (GitHub, bool, error) {
	if current.AppID != 0 {
		return current, true, nil
	}
	id, hasID := r.optional("github-app-id")
	installation, hasInstallation := r.optional("github-installation-id")
	repos, hasRepos := r.optional("github-repositories")
	set := 0
	for _, has := range []bool{hasID, hasInstallation, hasRepos} {
		if has {
			set++
		}
	}
	switch set {
	case 0:
		return GitHub{}, false, nil
	case 3:
	default:
		return GitHub{}, false, errors.New("give all three of github-app-id, github-installation-id and github-repositories, or none: the service stops at startup when only some are set")
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return GitHub{}, false, fmt.Errorf("github-app-id %q is not a number", id)
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(installation), 10, 64)
	if err != nil {
		return GitHub{}, false, fmt.Errorf("github-installation-id %q is not a number", installation)
	}
	var list []string
	for _, repo := range strings.Split(repos, ",") {
		list = append(list, strings.TrimSpace(repo))
	}
	owner, _, _ := strings.Cut(list[0], "/")
	gh := GitHub{AppID: appID, InstallationID: installationID, Owner: owner, Repositories: list}
	if err := gh.validate(); err != nil {
		return GitHub{}, false, err
	}
	return gh, true, nil
}

// readIsolatedAppKey reads the App's PEM from a file path, the answers file, a prompt for the path or stdin, and checks that it is
// an RSA private key. The fingerprint is the one GitHub shows next to the key. The key is never written to disk by us.
func readIsolatedAppKey(r *Resolver) (pemKey []byte, fingerprint string, err error) {
	var raw []byte
	_, flagged := r.optional("github-app-key")
	if file, ok := r.optional("github-app-key-file"); ok && file == "-" {
		raw, err = io.ReadAll(r.in)
	} else if ok {
		raw, err = os.ReadFile(filepath.Clean(file))
	} else if r.term.IsTerminal && !flagged {
		var file string
		if file, err = r.Value("github-app-key-file", "Path of the App's private key file (.pem)", ""); err == nil {
			raw, err = os.ReadFile(filepath.Clean(file))
		}
	} else {
		raw, err = r.Secret("github-app-key", "the GitHub App private key (PEM)")
	}
	if err != nil {
		return nil, "", fmt.Errorf("read the App key: %w", err)
	}
	if fingerprint, err = appKeyFingerprint(raw); err != nil {
		return nil, "", err
	}
	return append(bytes.TrimSpace(raw), '\n'), fingerprint, nil
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

// ensureSecrets creates the secrets that are missing (install.md 10.3, 10.7 and 10.8; the application role's one comes later).
// The database password is never regenerated once it exists: the database keeps it in its volume. Secrets that exist are
// not shape-checked again: the ones this installer generated are right by construction, and one made by hand (install.md 10.4
// checks its shape) is the administrator's to have checked. appKey is nil when the
// secret exists already.
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
// EnvironmentFile= makes a missing file fail the start. A file that exists is left alone unless there are new values.
func writeEnvFile(path string, gh GitHub, have bool) error {
	existing, err := os.ReadFile(filepath.Clean(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	exists := err == nil
	var content []byte
	switch {
	case have:
		content = fmt.Appendf(nil, "SUITEWARD_GITHUB_APP_ID=%d\nSUITEWARD_GITHUB_INSTALLATION_ID=%d\nSUITEWARD_GITHUB_REPOSITORIES=%s\n", gh.AppID, gh.InstallationID, strings.Join(gh.Repositories, ","))
		if exists && bytes.Equal(existing, content) {
			return nil
		}
	case exists:
		return nil
	}
	return writeFileAtomic(path, content, 0o700)
}

// dryRun runs the Podman generator's dry run (install.md 10.10) and checks that it listed the service of every unit: Podman
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
func waitHealthy(ctx context.Context, rt Runtime) error {
	deadline := time.Now().Add(dbHealthTimeout)
	for {
		state, err := rt.State(ctx, ContainerDB)
		last := state.Health
		if err != nil {
			last = err.Error()
		}
		if err == nil && state.Health == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the database was not healthy within %s (last: %q): read `journalctl --user -u suiteward-db.service -n 50 --no-pager`", dbHealthTimeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dbHealthInterval):
		}
	}
}

// ensureAppRole creates the application role and its URL secret once (install.md 10.11). The SQL, which holds the password,
// goes through stdin only, with the SET lines that keep it out of the database log. A role left by an earlier attempt gets
// the new password instead.
func ensureAppRole(ctx context.Context, rt Runtime) error {
	store := rt.Secrets()
	if have, err := store.Exists(ctx, SecretDatabaseURL); err != nil || have {
		return err
	}
	psql := func(extra []string, sql string) ([]byte, error) {
		argv := append(append([]string{"psql", "-X", "-q"}, extra...), "-v", "ON_ERROR_STOP=1", "-U", "suiteward", "-d", "suiteward", "-f", "-")
		return rt.Exec(ctx, ContainerDB, argv, strings.NewReader(sql))
	}
	found, err := psql([]string{"-t", "-A"}, "SELECT 1 FROM pg_roles WHERE rolname = '"+appDatabaseRole+"';\n")
	if err != nil {
		return fmt.Errorf("look for the role %s: %w", appDatabaseRole, err)
	}
	statement := "CREATE ROLE " + appDatabaseRole + " LOGIN PASSWORD"
	if strings.TrimSpace(string(found)) == "1" {
		statement = "ALTER ROLE " + appDatabaseRole + " PASSWORD"
	}
	password, err := NewPassword()
	if err != nil {
		return err
	}
	sql := "SET log_statement = 'none';\nSET log_min_error_statement = 'panic';\nSET log_min_duration_statement = -1;\n" + statement + " '" + password + "';\n"
	if _, err := psql(nil, sql); err != nil {
		return fmt.Errorf("create the role %s: %w", appDatabaseRole, err)
	}
	return store.Write(ctx, SecretDatabaseURL, []byte(DatabaseURL(appDatabaseRole, password)), false)
}
