package installer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// injectFirst makes every call whose argument line contains match fail, ahead of the rules the fake has.
func (f *fakeEngine) injectFirst(match string) {
	f.rules = append([]rule{{match: match, err: errInjected}}, f.rules...)
}

// A command of the account's machine that fails stops the isolated install at that step. The error says which step, no later step
// runs, and no secret value reaches an argument list.
func TestInstallIsolatedStopsAtTheFirstFailingCommand(t *testing.T) {
	cases := []struct {
		name     string
		match    string // the command the fake fails
		want     string // what the error says
		notAfter string // a command that must not have run
		setup    func(f *isoFake)
	}{
		{name: "the user id", match: "id -u", want: "find out who is running the installer", notAfter: "secret create"},
		{name: "the passwd entry", match: "getent passwd", want: "look up the account", notAfter: "secret create"},
		{name: "the user manager", match: "systemctl --user show --property=SystemState", want: "ask the systemd user manager", notAfter: "secret create"},
		{name: "linger", match: "loginctl show-user", want: "linger is not enabled", notAfter: "secret create"},
		{name: "the Podman connection", match: "podman info", want: "the Podman connection is unavailable", notAfter: "secret create"},
		{name: "pull the database image", match: "podman pull docker.io/library/postgres", want: errInjected.Error(), notAfter: "secret create", setup: func(f *isoFake) { f.noImage = true }},
		{name: "look for the database password", match: "secret ls --filter name=" + SecretDBAuth, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "look for the owner URL", match: "secret ls --filter name=" + SecretDatabaseOwnerURL, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "write the database password", match: "secret create " + SecretDBAuth, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "write the owner URL", match: "secret create " + SecretDatabaseOwnerURL, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "look for the token", match: "secret ls --filter name=" + SecretMCPAuth, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "write the token", match: "secret create " + SecretMCPAuth, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "write the App key", match: "secret create " + SecretGitHubAppKey, want: errInjected.Error(), notAfter: "daemon-reload"},
		{name: "the Podman generator", match: generatorPath, want: "the Podman generator rejects the units", notAfter: "daemon-reload"},
		{name: "the daemon reload", match: "daemon-reload", want: "systemctl --user daemon-reload", notAfter: "start suiteward-db.service"},
		{name: "the state of the service", match: "show --property=ActiveState --value suiteward.service", want: "ask for the state of suiteward.service", notAfter: "start suiteward-db.service"},
		{name: "the state of the database", match: "show --property=ActiveState --value suiteward-db.service", want: "ask for the state of suiteward-db.service", notAfter: "start suiteward-db.service"},
		{name: "the state of the migration", match: "show --property=ActiveState --value suiteward-migrate.service", want: "ask for the state of suiteward-migrate.service", notAfter: "start suiteward-db.service", setup: func(f *isoFake) { f.dbDown = true }},
		{name: "the restart of the network", match: "restart suiteward-network.service", want: "restart the network", notAfter: "start suiteward-db.service", setup: func(f *isoFake) { f.dbDown, f.migrateDown = true, true }},
		{name: "the start of the database", match: "start suiteward-db.service", want: "start the database", notAfter: "psql"},
		{name: "the look for the role", match: "exec -i suiteward-db psql", want: "look for the role", notAfter: "start suiteward.service"},
		{name: "the start of a running service", match: "--user start suiteward.service", want: "start the service", setup: func(f *isoFake) { f.serviceRunning = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIsoFake(t.TempDir())
			if tc.setup != nil {
				tc.setup(f)
			}
			f.failOn[tc.match] = errInjected
			_, err := runIsolated(t, f, nil, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want an error containing %q", err, tc.want)
			}
			if tc.notAfter != "" && f.has(tc.notAfter) {
				t.Errorf("%q ran after the failure:\n%s", tc.notAfter, strings.Join(f.lines(), "\n"))
			}
			f.secretValuesNotInArgs(t)
		})
	}
}

// The database that never reports healthy ends the wait with its last answer, and a cancelled install does not keep waiting.
func TestWaitHealthyEndsWithTheLastAnswerOrTheContext(t *testing.T) {
	fastHealthWait(t)
	f := newFake("linux").on("info --format", "true\n")
	rt := newRootlessPodman(t, f, "")
	f.on("inspect --type container --format {{json .State}}", `{"Status":"running","Health":{"Status":"starting"}}`)
	if err := waitHealthy(context.Background(), rt, nil); err == nil || !strings.Contains(err.Error(), `last: "starting"`) {
		t.Errorf("waitHealthy = %v", err)
	}

	f.injectFirst("inspect --type container --format {{json .State}}")
	if err := waitHealthy(context.Background(), rt, nil); err == nil || !strings.Contains(err.Error(), errInjected.Error()) {
		t.Errorf("waitHealthy with a failing inspect = %v", err)
	}

	old := dbHealthTimeout
	dbHealthTimeout = time.Hour
	t.Cleanup(func() { dbHealthTimeout = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHealthy(ctx, rt, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("waitHealthy with a cancelled context = %v", err)
	}
}

func TestEnsureAppRoleStopsWhenTheRoleCannotBeLookedFor(t *testing.T) {
	f := newFake("linux").on("info --format", "true\n")
	rt := newRootlessPodman(t, f, "")
	f.injectFirst("exec -i suiteward-db psql")
	if err := ensureAppRole(context.Background(), rt); err == nil || !strings.Contains(err.Error(), "look for the role "+appRole) {
		t.Errorf("ensureAppRole = %v", err)
	}
	f.injectFirst("secret ls --filter name=" + SecretDatabaseURL)
	if err := ensureAppRole(context.Background(), rt); !errors.Is(err, errInjected) {
		t.Errorf("ensureAppRole with a failing lookup of the URL = %v", err)
	}
}

// ---- before anything is changed ----

func TestOpenIsolatedHostRefusesWhatItCannotUse(t *testing.T) {
	if _, err := openIsolatedHost(context.Background(), isolatedSettings(), Deps{GOOS: "linux", Getenv: func(string) string { return "" }}); err == nil {
		t.Error("a host was opened without a way to run commands")
	}
	f := newIsoFake(t.TempDir())
	bad := isolatedSettings()
	bad.HTTPPort = 0
	if _, err := openIsolatedHost(context.Background(), bad, f.deps()); err == nil {
		t.Error("a host was opened for invalid settings")
	}
	if f.has("podman") {
		t.Errorf("podman was asked for settings that are invalid: %v", f.lines())
	}
}

func TestCheckAccountNamesWhatIsWrong(t *testing.T) {
	home := t.TempDir() // absolute on every system; a colon in it (a drive) must not split the passwd line
	answers := func(over map[string]func() (string, error)) func(string, ...string) (string, error) {
		return func(name string, args ...string) (string, error) {
			key := name + " " + strings.Join(args, " ")
			if f, ok := over[key]; ok {
				return f()
			}
			switch key {
			case "id -u":
				return "1001", nil
			case "id -un":
				return ServiceAccount, nil
			case "getent passwd " + ServiceAccount:
				return ServiceAccount + ":x:1001:1001::" + home + ":/usr/sbin/nologin", nil
			}
			return "", errors.New("unexpected " + key)
		}
	}
	fail := func() (string, error) { return "", errInjected }
	cases := []struct {
		name string
		over map[string]func() (string, error)
		home string
		want string
	}{
		{"the account name cannot be found", map[string]func() (string, error){"id -un": fail}, home, "find the account name"},
		{"the passwd entry cannot be found", map[string]func() (string, error){"getent passwd " + ServiceAccount: fail}, home, "look up the account"},
		{"the passwd entry is short", map[string]func() (string, error){"getent passwd " + ServiceAccount: func() (string, error) { return "suiteward:x:1001", nil }}, home, "is not a passwd line"},
		{"HOME is not the account's home", nil, filepath.Join(t.TempDir(), "other"), "HOME is"},
		{"HOME is not absolute", nil, "home", "HOME is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checkAccount(answers(tc.over), tc.home); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkAccount = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if got, err := checkAccount(answers(nil), home); err != nil || got != home {
		t.Errorf("checkAccount = %q, %v", got, err)
	}
}

func TestStackNeverStartedReadsTheSettingsAndTheService(t *testing.T) {
	never := func(out string, err error) func(string, ...string) (string, error) {
		return func(string, ...string) (string, error) { return out, err }
	}
	missing := filepath.Join(t.TempDir(), "settings.yaml")
	if got, err := stackNeverStarted(never("", nil), missing); err != nil || !got {
		t.Errorf("no settings and no start: %v, %v", got, err)
	}
	if got, err := stackNeverStarted(never("Thu 2026-10-22", nil), missing); err != nil || got {
		t.Errorf("no settings but a start: %v, %v", got, err)
	}
	if _, err := stackNeverStarted(never("", errInjected), missing); err == nil || !strings.Contains(err.Error(), "ask when suiteward.service was last active") {
		t.Errorf("a failing systemctl: %v", err)
	}
	if _, err := stackNeverStarted(never("", nil), t.TempDir()+"/bad\x00name"); err == nil || !strings.Contains(err.Error(), "look for") {
		t.Errorf("a settings path that cannot be examined: %v", err)
	}
	present := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := stackNeverStarted(never("", errInjected), present); err != nil || got {
		t.Errorf("settings that exist mean a start was completed: %v, %v", got, err)
	}
}

func TestCheckUserManagerNamesWhatIsWrong(t *testing.T) {
	run := func(state, linger string, failID bool) func(string, ...string) (string, error) {
		return func(name string, args ...string) (string, error) {
			switch name {
			case "systemctl":
				return state, nil
			case "id":
				if failID {
					return "", errInjected
				}
				return ServiceAccount, nil
			}
			return linger, nil
		}
	}
	if err := checkUserManager(run("running", "yes", false)); err != nil {
		t.Errorf("a ready manager: %v", err)
	}
	if err := checkUserManager(run("degraded", "yes", false)); err != nil {
		t.Errorf("a degraded manager is accepted: %v", err)
	}
	if err := checkUserManager(run("stopping", "yes", false)); err == nil || !strings.Contains(err.Error(), `is "stopping"`) {
		t.Errorf("a stopping manager: %v", err)
	}
	if err := checkUserManager(run("running", "yes", true)); err == nil || !strings.Contains(err.Error(), "find the account name") {
		t.Errorf("no account name: %v", err)
	}
	if err := checkUserManager(run("running", "no", false)); err == nil || !strings.Contains(err.Error(), "linger is not enabled") {
		t.Errorf("no linger: %v", err)
	}
}

// ---- what is installed already ----

func TestAdoptUnitsDirRefusesAUnitItCannotRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "suiteward.container"), 0o700); err != nil { // a directory where the unit file should be
		t.Fatal(err)
	}
	if _, err := adoptUnitsDir(dir, filepath.Join(dir, "github.env"), false); err == nil || !strings.Contains(err.Error(), "read the installed unit suiteward.container") {
		t.Fatalf("adoptUnitsDir = %v", err)
	}
}

// Only the tier is reported: a port or the job timeout differs from the installed one only when the run was not given it, and then
// nothing was asked for. One that is given wins and is said by sayChanges.
func TestReportAdoptedSaysOnlyWhichTierReplacesTheOneAskedFor(t *testing.T) {
	var said []string
	say := func(format string, args ...any) {
		said = append(said, strings.TrimSpace(strings.ReplaceAll(format, "\n", " ")))
	}
	asked, installed := DefaultSettings(), DefaultSettings()
	reportAdopted(say, asked, installed)
	if len(said) != 0 {
		t.Fatalf("equal settings reported %v", said)
	}
	installed.HTTPPort, installed.MCPPort, installed.JobTimeout = asked.HTTPPort+1, asked.MCPPort+1, "9m"
	reportAdopted(say, asked, installed)
	if len(said) != 0 {
		t.Fatalf("a port or a job timeout that was not asked for was reported: %v", said)
	}
	asked.Tier, installed.Tier = config.TierCoLocated, config.TierIsolated
	reportAdopted(say, asked, installed)
	if len(said) != 1 || !strings.Contains(said[0], "installation type that is installed") {
		t.Fatalf("said %v, want the installed tier alone", said)
	}
}

func TestChooseImageStopsWhenItCannotTellWhetherTheStackEverStarted(t *testing.T) {
	r := newTestResolver(t, map[string]string{"image": "sha256:" + strings.Repeat("b", 64)}, nil, Terminal{In: strings.NewReader(""), Out: &syncBuffer{}})
	_, err := chooseImage("suiteward", r, strings.Repeat("a", 64), func() (bool, error) { return false, errInjected }, func(string, ...any) {})
	if !errors.Is(err, errInjected) {
		t.Fatalf("chooseImage = %v", err)
	}
}

// ---- the files of the account ----

func TestEnvFilesThatCannotBeReadAreNamed(t *testing.T) {
	dir := t.TempDir()
	if _, err := readGitHubEnvFile(dir); err == nil || !strings.Contains(err.Error(), "read "+dir) {
		t.Errorf("readGitHubEnvFile of a directory = %v", err)
	}
	if _, err := writeEnvFile(dir, GitHub{}, false); err == nil || !strings.Contains(err.Error(), "read "+dir) {
		t.Errorf("writeEnvFile over a directory = %v", err)
	}
}

func TestReadGitHubEnvFileRefusesValuesThatAreNotNumbers(t *testing.T) {
	write := func(t *testing.T, body string) string {
		path := filepath.Join(t.TempDir(), "github.env")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := "SUITEWARD_GITHUB_APP_ID=%s\nSUITEWARD_GITHUB_INSTALLATION_ID=%s\nSUITEWARD_GITHUB_REPOSITORIES=acme/api\n"
	for _, tc := range []struct{ app, inst, want string }{
		{"abc", "2", envAppID + " is not a positive number"},
		{"0", "2", envAppID + " is not a positive number"},
		{"1", "abc", envInstallationID + " is not a positive number"},
		{"1", "-5", envInstallationID + " is not a positive number"},
	} {
		body := strings.NewReplacer("%s", "\x00").Replace(base)
		body = strings.Replace(body, "\x00", tc.app, 1)
		body = strings.Replace(body, "\x00", tc.inst, 1)
		if _, err := readGitHubEnvFile(write(t, body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("app %q installation %q: %v, want %q", tc.app, tc.inst, err, tc.want)
		}
	}
}

// A key that is not usable is refused before any change, and the refusal names what is wrong with it, never its content.
func TestAppKeyFingerprintRefusesWhatIsNotAnRSAKey(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		pem  []byte
		want string
	}{
		"not PEM":       {[]byte("nothing secret-xyz"), "not a PEM private key"},
		"a certificate": {pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("secret-xyz")}), `is a "CERTIFICATE"`},
		"garbage PKCS1": {pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("secret-xyz")}), "does not parse"},
		"garbage PKCS8": {pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret-xyz")}), "does not parse"},
		"an EC key":     {pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), "not an RSA key"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := appKeyFingerprint(tc.pem)
			if err == nil || !strings.Contains(err.Error(), tc.want) || got != "" {
				t.Fatalf("appKeyFingerprint = %q, %v; want an error containing %q", got, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-xyz") {
				t.Errorf("the key content reached the error: %v", err)
			}
		})
	}
}

// ensureSecrets reads and writes the secrets of the stack in order; each failing call stops it and no later secret is written.
func TestEnsureSecretsStopsAtTheFirstFailingCall(t *testing.T) {
	cases := []struct {
		name     string
		have     []string // secrets that exist
		fail     string
		notAfter string
		hidden   bool // a read never carries the engine's message: it may quote the value
	}{
		{"look for the password", nil, "secret ls --filter name=" + SecretDBAuth, "secret create", false},
		{"look for the owner URL", nil, "secret ls --filter name=" + SecretDatabaseOwnerURL, "secret create", false},
		{"read the stored password", []string{SecretDBAuth}, "secret inspect --showsecret --format {{.SecretData}} " + SecretDBAuth, "secret create", true},
		{"write the password", nil, "secret create " + SecretDBAuth, "secret create " + SecretDatabaseOwnerURL, false},
		{"write the owner URL", nil, "secret create " + SecretDatabaseOwnerURL, "secret create " + SecretMCPAuth, false},
		{"look for the token", []string{SecretDBAuth, SecretDatabaseOwnerURL}, "secret ls --filter name=" + SecretMCPAuth, "secret create", false},
		{"write the token", []string{SecretDBAuth, SecretDatabaseOwnerURL}, "secret create " + SecretMCPAuth, "secret create " + SecretGitHubAppKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake("linux").on("info --format", "true\n")
			rt := newRootlessPodman(t, f, "")
			for _, name := range tc.have {
				f.on("secret ls --filter name="+name, name+"\n")
			}
			f.injectFirst(tc.fail)
			err := ensureSecrets(context.Background(), rt.Secrets(), []byte("key"))
			switch {
			case err == nil:
				t.Fatal("ensureSecrets succeeded although a call failed")
			case tc.hidden:
				if errors.Is(err, errInjected) || !strings.Contains(err.Error(), "read the secret "+SecretDBAuth) {
					t.Fatalf("ensureSecrets = %v, want the read named without the engine's message", err)
				}
			case !errors.Is(err, errInjected):
				t.Fatalf("ensureSecrets = %v, want the injected failure", err)
			}
			if len(f.find(tc.notAfter)) != 0 {
				t.Errorf("%q ran after the failure: %v", tc.notAfter, f.lines())
			}
		})
	}
}

// The install builds the files of the account where they go; when a place is taken by a directory it cannot read as a file, it stops
// and names the place before it changes anything.
func TestInstallIsolatedStopsWhenAFilePlaceIsTaken(t *testing.T) {
	t.Run("the env file is a directory", func(t *testing.T) {
		home := t.TempDir()
		place := envFile(home)
		if err := os.MkdirAll(place, 0o700); err != nil {
			t.Fatal(err)
		}
		f := newIsoFake(home)
		g := isoGitHubOn(t, f)
		g.answers = map[string]string{"github-app-id": "", "github-installation-id": "", "github-repositories": "", "github-owner": ""}
		err := g.run()
		if err == nil || !strings.Contains(err.Error(), place) {
			t.Fatalf("install = %v, want an error naming %s", err, place)
		}
		if f.has("secret create") || f.has("daemon-reload") {
			t.Errorf("something was changed although the env file could not be read: %v", f.lines())
		}
	})
	t.Run("the units directory is a file", func(t *testing.T) {
		home := t.TempDir()
		place := unitDir(home)
		if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(place, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		f := newIsoFake(home)
		_, err := runIsolated(t, f, nil, "")
		if err == nil || !strings.Contains(err.Error(), place) {
			t.Fatalf("install = %v, want an error naming %s", err, place)
		}
		if f.has("daemon-reload") {
			t.Errorf("the units were loaded although their place is taken: %v", f.lines())
		}
	})
}

func TestInstallIsolatedRefusesAMalformedEnvFileBeforeChangingAnything(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newIsoFake(home)
	g := isoGitHubOn(t, f)
	g.answers = map[string]string{"github-app-id": "", "github-installation-id": "", "github-repositories": "", "github-owner": ""}
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), "has some of the three GitHub values") {
		t.Fatalf("install = %v", err)
	}
	if f.has("secret create") {
		t.Errorf("a secret was created for an env file that was refused: %v", f.lines())
	}
}

// An App whose key cannot be used, or that has no key to store, is refused before the stack is changed.
func TestInstallIsolatedRefusesAKeyItCannotStore(t *testing.T) {
	t.Run("the key is not a key", func(t *testing.T) {
		f := newIsoFake(t.TempDir())
		g := isoGitHubOn(t, f)
		flow := g.flow
		g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
			res, err := flow(ctx, e)
			res.PrivateKey = []byte("not a pem secret-xyz")
			return res, err
		}
		err := g.run()
		if err == nil || !strings.Contains(err.Error(), "not a PEM private key") || strings.Contains(err.Error(), "secret-xyz") {
			t.Fatalf("install = %v", err)
		}
		if f.has("secret create") {
			t.Errorf("a secret was created for a key that was refused: %v", f.lines())
		}
	})
	t.Run("the secret of the key cannot be looked for", func(t *testing.T) {
		f := newIsoFake(t.TempDir())
		g := isoGitHubOn(t, f)
		flow := g.flow
		g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
			res, err := flow(ctx, e)
			res.PrivateKey = nil
			return res, err
		}
		f.failOn["secret ls --filter name="+SecretGitHubAppKey] = errInjected
		if err := g.run(); !errors.Is(err, errInjected) {
			t.Fatalf("install = %v", err)
		}
		if f.has("secret create") {
			t.Errorf("a secret was created before the key was known to be there: %v", f.lines())
		}
	})
}

// writeEnvFile says whether the values a service reads from the file changed.
func TestWriteEnvFileSaysWhetherTheValuesChanged(t *testing.T) {
	gh := GitHub{AppID: 123, InstallationID: 456, Repositories: []string{"octo/one"}}
	path := filepath.Join(t.TempDir(), "github.env")
	step := func(name string, gh GitHub, have, wantChanged bool) {
		t.Helper()
		changed, err := writeEnvFile(path, gh, have)
		if err != nil || changed != wantChanged {
			t.Errorf("%s: changed = %v, %v; want %v", name, changed, err, wantChanged)
		}
	}
	step("a file that did not exist is made empty: no values", GitHub{}, false, false)
	step("an empty file stays", GitHub{}, false, false)
	step("values over an empty file", gh, true, true)
	step("the same values", gh, true, false)
	step("an existing file is left alone without values", GitHub{}, false, false)
	step("other values", GitHub{AppID: 124, InstallationID: 456, Repositories: []string{"octo/one"}}, true, true)
}
