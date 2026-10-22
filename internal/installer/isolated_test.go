package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// isoFake is the service account's machine: systemd user manager, rootless Podman with a secret store, and the database
// container. It answers from its state and records every call.
type isoFake struct {
	calls []call
	goos  string
	euid  string

	systemState string // answer of the user manager
	linger      string
	health      string // health of suiteward-db once started
	roleExists  bool
	secrets     map[string]string // existing Podman secrets and their values
	dryRunSkips string            // a generated unit the dry run leaves out
	failOn      map[string]error  // command name + " " + argument text -> error
	active      map[string]string
	home        string

	user           string // who `id -un` says is running (default: the service account)
	passwdHome     string // the home the passwd entry of the account holds (default: home)
	everActive     bool   // suiteward.service was active once before this run
	serviceRunning bool   // suiteward.service is active when the run begins
	started        bool   // suiteward.service was started by this run
}

func newIsoFake(home string) *isoFake {
	return &isoFake{goos: "linux", euid: "1001", systemState: "running", linger: "yes", health: "healthy", secrets: map[string]string{}, failOn: map[string]error{}, active: map[string]string{}, home: home, user: ServiceAccount, passwdHome: home}
}

var generatedUnits = []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service", "suiteward-network.service", "suiteward-data-volume.service", "suiteward-db-data-volume.service"}

func (f *isoFake) deps() Deps {
	return Deps{
		GOOS:        f.goos,
		Getenv:      func(k string) string { return map[string]string{"HOME": f.home}[k] },
		Interactive: func(context.Context, string, []string) error { return nil },
		Exec: func(_ context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
			c := call{name: name, args: append([]string(nil), args...)}
			if stdin != nil {
				data, err := io.ReadAll(stdin)
				if err != nil {
					return nil, err
				}
				c.stdin = string(data)
			}
			f.calls = append(f.calls, c)
			for match, err := range f.failOn {
				if strings.Contains(name+" "+c.line(), match) {
					return nil, err
				}
			}
			return f.answer(c)
		},
	}
}

func (f *isoFake) answer(c call) ([]byte, error) {
	line := c.line()
	switch {
	case c.name == "id" && line == "-u":
		return []byte(f.euid + "\n"), nil
	case c.name == "id" && line == "-un":
		return []byte(f.user + "\n"), nil
	case c.name == "getent":
		return []byte("suiteward:x:1001:1001::" + f.passwdHome + ":/usr/sbin/nologin\n"), nil
	case c.name == "systemctl" && strings.HasPrefix(line, "--user show --property=ActiveEnterTimestamp"):
		if f.everActive {
			return []byte("Thu 2026-10-22 10:00:00 UTC\n"), nil
		}
		return []byte("\n"), nil
	case c.name == "systemctl" && line == "--user start suiteward.service":
		f.started = true
	case c.name == "systemctl" && strings.HasPrefix(line, "--user show --property=SystemState"):
		return []byte(f.systemState + "\n"), nil
	case c.name == "systemctl" && strings.HasPrefix(line, "--user show --property=ActiveState --value "):
		unit := strings.TrimPrefix(line, "--user show --property=ActiveState --value ")
		if state, ok := f.active[unit]; ok {
			return []byte(state + "\n"), nil
		}
		if unit == "suiteward.service" && !f.serviceRunning && !f.started {
			return []byte("inactive\n"), nil
		}
		return []byte("active\n"), nil
	case c.name == "loginctl":
		return []byte(f.linger + "\n"), nil
	case c.name == generatorPath:
		var out strings.Builder
		for _, unit := range generatedUnits {
			if unit != f.dryRunSkips {
				out.WriteString("---" + unit + "---\n[Service]\n")
			}
			if unit == "suiteward-migrate.service" { // the units name each other, so a skipped unit's name still shows up
				out.WriteString("Requires=suiteward-db.service\nAfter=suiteward-network.service\n")
			}
		}
		return []byte(out.String()), nil
	case c.name == "podman":
		return f.answerPodman(c)
	}
	return nil, nil
}

// answerPodman answers the podman commands: the secret store, the database container and psql.
func (f *isoFake) answerPodman(c call) ([]byte, error) {
	line := c.line()
	switch {
	case strings.HasPrefix(line, "info "):
		return []byte("true\n"), nil
	case strings.HasPrefix(line, "secret ls --filter name="):
		name := strings.Fields(strings.TrimPrefix(line, "secret ls --filter name="))[0]
		if _, ok := f.secrets[name]; ok {
			return []byte(name + "\n"), nil
		}
	case strings.HasPrefix(line, "secret inspect --showsecret"):
		return []byte(f.secrets[c.args[len(c.args)-1]] + "\n"), nil
	case strings.HasPrefix(line, "secret create"):
		f.secrets[c.args[len(c.args)-2]] = c.stdin
	case strings.HasPrefix(line, "inspect --type container --format {{json .State}}"):
		return []byte(`{"Status":"running","Health":{"Status":"` + f.health + `"}}`), nil
	case strings.HasPrefix(line, "exec -i suiteward-db psql") && strings.Contains(line, "-t -A"):
		if f.roleExists {
			return []byte("1\n"), nil
		}
	}
	return nil, nil
}

func (f *isoFake) lines() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.name+" "+c.line())
	}
	return out
}

// index is the position of the first call from `from` whose command line contains text, or -1.
func (f *isoFake) index(text string, from int) int {
	for i := from; i < len(f.calls); i++ {
		if strings.Contains(f.calls[i].name+" "+f.calls[i].line(), text) {
			return i
		}
	}
	return -1
}

func (f *isoFake) has(text string) bool { return f.index(text, 0) >= 0 }

var (
	isoKeyOnce sync.Once
	isoKeyPEM  string
)

// appKeyPEM is a throwaway RSA key in the shape of a GitHub App key.
func appKeyPEM(t *testing.T) string {
	t.Helper()
	isoKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		isoKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	})
	return isoKeyPEM
}

// appKeyStored is what the secret store should hold for the key: the PEM with exactly one trailing newline.
func appKeyStored(t *testing.T) string { return strings.TrimSpace(appKeyPEM(t)) + "\n" }

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func isolatedSettings() Settings {
	s := DefaultSettings()
	s.Tier = config.TierIsolated
	return s
}

func unitDir(home string) string { return filepath.Join(home, ".config", "containers", "systemd") }

func settingsFile(home string) string {
	return filepath.Join(home, ".config", "suiteward", "settings.yaml")
}

func envFile(home string) string { return filepath.Join(home, ".config", "suiteward", "github.env") }

// runIsolated runs installIsolated with the image and the App key given as answers unless the test overrides them.
func runIsolated(t *testing.T, f *isoFake, answers map[string]string, stdin string) (string, error) {
	t.Helper()
	merged := map[string]string{"image": testDigest, "github-app-key": appKeyPEM(t)}
	for k, v := range answers {
		if v == "" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	term, _ := fakeTerminal(false, stdin)
	r := newTestResolver(t, nil, merged, term)
	var out bytes.Buffer
	err := installIsolated(context.Background(), isolatedSettings(), r, f.deps(), &out)
	return out.String(), err
}

func fastHealthWait(t *testing.T) {
	t.Helper()
	oldTimeout, oldInterval := dbHealthTimeout, dbHealthInterval
	dbHealthTimeout, dbHealthInterval = 30*time.Millisecond, time.Millisecond
	t.Cleanup(func() { dbHealthTimeout, dbHealthInterval = oldTimeout, oldInterval })
}

func TestInstallIsolatedRefusesOtherSystemsAndRoot(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.goos = "darwin"
	if _, err := runIsolated(t, f, nil, ""); err == nil || len(f.calls) != 0 {
		t.Fatalf("a non-Linux system must be refused before any command: %v, %v", err, f.lines())
	}

	f = newIsoFake(t.TempDir())
	f.euid = "0"
	if _, err := runIsolated(t, f, nil, ""); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("root must be refused naming root, got %v", err)
	}
	if f.has("secret create") || f.has("start") {
		t.Fatalf("a refused run changed something: %v", f.lines())
	}
}

func TestInstallIsolatedFreshInstallRunsEveryStepInOrder(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	out, err := runIsolated(t, f, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	assertSecretShapes(t, f)

	// No secret value in any argument list: I3's helper over the values, and the check of this file over the SQL.
	var values []string
	for _, v := range f.secrets {
		values = append(values, v)
	}
	(&fakeEngine{calls: f.calls}).assertNoSecret(t, values...)
	f.secretValuesNotInArgs(t)

	// Order: pulls and units, the dry run, the reload, the database, its health, the role, the service, the check.
	pull := f.index("podman pull "+releaseImageRepo+"@"+testDigest, 0)
	dry := f.index(generatorPath+" --user --dryrun", pull)
	reload := f.index("systemctl --user daemon-reload", dry)
	startDB := f.index("systemctl --user start suiteward-db.service", reload)
	health := f.index("inspect --type container --format {{json .State}} suiteward-db", startDB)
	role := f.index("exec -i suiteward-db psql -X -q -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f -", health)
	appURL := f.index("secret create "+SecretDatabaseURL+" -", role)
	startSvc := f.index("systemctl --user start suiteward.service", appURL)
	check := f.index("--property=ActiveState --value suiteward.service", startSvc)
	if f.index("podman pull docker.io/library/postgres", 0) < 0 {
		t.Errorf("the database image must be pulled by digest: %v", f.lines())
	}
	for name, i := range map[string]int{"pull": pull, "dry run": dry, "reload": reload, "start db": startDB, "health": health, "role": role, "app url": appURL, "start service": startSvc, "check": check} {
		if i < 0 {
			t.Fatalf("step %q is missing or out of order:\n%s", name, strings.Join(f.lines(), "\n"))
		}
	}
	for _, unit := range []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service"} {
		if !f.has("--property=ActiveState --value " + unit) {
			t.Errorf("%s must be checked after the start", unit)
		}
	}

	// The role SQL: the SET lines first, the password in the statement, on stdin only.
	sql := f.calls[role].stdin
	re := regexp.MustCompile(`(?s)^SET log_statement = 'none';\nSET log_min_error_statement = 'panic';\nSET log_min_duration_statement = -1;\nCREATE ROLE suiteward_app LOGIN PASSWORD '([0-9a-f]{48})';\n$`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("the role SQL has the wrong shape: %q", sql)
	}
	if !strings.Contains(f.secrets[SecretDatabaseURL], ":"+m[1]+"@") {
		t.Errorf("the application URL must carry the password of the role")
	}

	assertFilesAndSettings(t, home)

	for _, want := range []string{"podman exec -it suiteward /suiteward setup", "SHA256:", "suiteward doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
}

// secretValuesNotInArgs fails when a secret value reached an argument list.
func (f *isoFake) secretValuesNotInArgs(t *testing.T) {
	t.Helper()
	for _, c := range f.calls {
		for _, arg := range c.args {
			for _, v := range f.secrets {
				if len(v) >= 16 && strings.Contains(arg, v) {
					t.Errorf("a secret value reached the arguments of %s: %v", c.name, c.args)
				}
			}
			if strings.Contains(arg, "PASSWORD") {
				t.Errorf("SQL reached the arguments of %s: %v", c.name, c.args)
			}
		}
	}
}

func TestInstallIsolatedAltersAnExistingRoleInsteadOfCreatingIt(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.roleExists = true
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, c := range f.calls {
		if strings.Contains(c.line(), "-f -") && c.stdin != "" {
			sql = c.stdin
		}
	}
	if !strings.Contains(sql, "ALTER ROLE suiteward_app PASSWORD '") || strings.Contains(sql, "CREATE ROLE") {
		t.Fatalf("an existing role gets a new password: %q", sql)
	}
}

func TestInstallIsolatedRerunCreatesNothingTwice(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	units, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	before := len(f.calls)
	if _, err := runIsolated(t, f, map[string]string{"github-app-key": ""}, ""); err != nil {
		t.Fatalf("a re-run must succeed, and must not ask for the App key again: %v", err)
	}
	second := &isoFake{calls: f.calls[before:]}
	if second.has("secret create") || second.has("psql") {
		t.Fatalf("a re-run created something again:\n%s", strings.Join(second.lines(), "\n"))
	}
	again, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	if !bytes.Equal(units, again) {
		t.Errorf("a re-run changed the unit")
	}
	if !second.has(generatorPath) || !second.has("daemon-reload") || !second.has("start suiteward.service") {
		t.Errorf("a re-run still checks and starts the stack: %v", second.lines())
	}
}

func TestInstallIsolatedRebuildsTheOwnerURLFromAnExistingPassword(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.secrets[SecretDBAuth] = strings.Repeat("ab", 24)
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	if f.has("secret create " + SecretDBAuth) {
		t.Fatalf("an existing password must never be replaced (the database keeps it): %v", f.lines())
	}
	if f.secrets[SecretDatabaseOwnerURL] != DatabaseURL("suiteward", strings.Repeat("ab", 24)) {
		t.Fatalf("the owner URL must be rebuilt from the stored password: %q", f.secrets[SecretDatabaseOwnerURL])
	}

	f = newIsoFake(t.TempDir())
	f.secrets[SecretDatabaseOwnerURL] = DatabaseURL("suiteward", strings.Repeat("cd", 24))
	if _, err := runIsolated(t, f, nil, ""); err == nil || f.has("start") {
		t.Fatalf("an owner URL without its password cannot be repaired silently: %v", err)
	}
}

func TestInstallIsolatedRequiresAnImageDigestBeforeChangingAnything(t *testing.T) {
	for name, image := range map[string]string{"none": "", "tag": "latest", "short": "sha256:abc"} {
		home := t.TempDir()
		f := newIsoFake(home)
		_, err := runIsolated(t, f, map[string]string{"image": image}, "")
		if err == nil || !strings.Contains(err.Error(), "image") {
			t.Fatalf("%s: the run needs --image sha256:<digest>, got %v", name, err)
		}
		if f.has("secret create") || f.has("pull") || f.has("start") {
			t.Fatalf("%s: nothing may happen before the image is known: %v", name, f.lines())
		}
		if _, statErr := os.Stat(filepath.Join(home, ".config")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: no file may be written: %v", name, statErr)
		}
	}
}

func TestInstallIsolatedAcceptsTheFullImageReference(t *testing.T) {
	f := newIsoFake(t.TempDir())
	if _, err := runIsolated(t, f, map[string]string{"image": testImage}, ""); err != nil {
		t.Fatal(err)
	}
	if !f.has("podman pull " + testImage) {
		t.Fatalf("the digest of the full reference must be pinned: %v", f.lines())
	}
}

func TestInstallIsolatedStopsWhenTheUserManagerOrLingerIsNotReady(t *testing.T) {
	for name, mutate := range map[string]func(*isoFake){
		"user manager": func(f *isoFake) { f.systemState = "offline" },
		"linger":       func(f *isoFake) { f.linger = "no" },
	} {
		home := t.TempDir()
		f := newIsoFake(home)
		mutate(f)
		if _, err := runIsolated(t, f, nil, ""); err == nil {
			t.Fatalf("%s: the run must stop", name)
		}
		if f.has("secret create") || f.has("pull") || f.has("start") {
			t.Fatalf("%s: nothing may change: %v", name, f.lines())
		}
	}

	f := newIsoFake(t.TempDir())
	f.systemState = "degraded"
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatalf("a degraded user manager is accepted as install.md 10.2 says: %v", err)
	}
}

func TestInstallIsolatedAcceptsRootlessPodmanOnly(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.failOn["info "] = errors.New("podman: cannot connect")
	if _, err := runIsolated(t, f, nil, ""); err == nil || f.has("secret create") {
		t.Fatalf("an unusable Podman must stop the run: %v", err)
	}
}

// installedUnits renders the units of this release into the account's unit directory, as an earlier install did.
func installedUnits(t *testing.T, home string, p UnitParams) {
	t.Helper()
	units, err := RenderUnits(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteUnits(unitDir(home), units); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIsolatedAdoptionStopsWithTheDiffBeforeAnyChange(t *testing.T) {
	home := t.TempDir()
	p := isolatedParams()
	p.EnvFile = "%h/.config/suiteward/github.env"
	installedUnits(t, home, p)
	path := filepath.Join(unitDir(home), "suiteward-db.container")
	edited := slices.Concat(readFileOrFail(t, path), []byte("PodmanArgs=--privileged\n"))
	if err := os.WriteFile(filepath.Clean(path), edited, 0o600); err != nil {
		t.Fatal(err)
	}

	f := newIsoFake(home)
	_, err := runIsolated(t, f, nil, "")
	var diff *DiffError
	if !errors.As(err, &diff) {
		t.Fatalf("an edited unit must stop the run with the diff, got %v", err)
	}
	if f.has("secret create") || f.has("pull") || f.has("start") || f.has("daemon-reload") {
		t.Fatalf("nothing may change after a DiffError: %v", f.lines())
	}
	if !bytes.Equal(readFileOrFail(t, path), edited) {
		t.Errorf("the edited unit was overwritten")
	}
	if _, err := os.Stat(settingsFile(home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("settings must not be saved after a DiffError: %v", err)
	}
}

func TestInstallIsolatedAdoptsAnInstallAndCarriesItsFields(t *testing.T) {
	home := t.TempDir()
	other := "sha256:" + strings.Repeat("b", 64)
	p := UnitParams{Digest: other, Tier: config.TierIsolated, HTTPPort: 9081, MCPPort: 9091, EnvFile: DefaultEnvFile}
	installedUnits(t, home, p)

	f := newIsoFake(home)
	out, err := runIsolated(t, f, map[string]string{"image": ""}, "")
	if err != nil {
		t.Fatalf("an adopted install needs no --image: %v", err)
	}
	if !strings.Contains(out, "9081") || !strings.Contains(out, "8081") {
		t.Errorf("an adopted port that replaces the requested one must be reported, with both values:\n%s", out)
	}
	serve, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	if !strings.Contains(string(serve), "127.0.0.1:9081:8080") || !strings.Contains(string(serve), "@"+other) {
		t.Errorf("the installed ports and digest must be carried:\n%s", serve)
	}
	if saved, err := LoadSettings(settingsFile(home)); err != nil || saved.HTTPPort != 9081 || saved.MCPPort != 9091 {
		t.Errorf("the carried ports must reach the settings: %+v, %v", saved, err)
	}

	f = newIsoFake(home)
	if _, err := runIsolated(t, f, nil, ""); err == nil || !strings.Contains(err.Error(), "--upgrade") {
		t.Fatalf("a different digest than the installed one is an upgrade, which has its own command: %v", err)
	}
	if f.has("secret create") || f.has("start") {
		t.Fatalf("nothing may change before that refusal: %v", f.lines())
	}
}

func TestInstallIsolatedLetsAFailedFirstRunRetryWithAnotherImage(t *testing.T) {
	other := "sha256:" + strings.Repeat("b", 64)
	retry := func(t *testing.T, mutate func(home string, f *isoFake)) (string, *isoFake, string, error) {
		t.Helper()
		home := t.TempDir()
		installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, EnvFile: DefaultEnvFile})
		f := newIsoFake(home)
		mutate(home, f)
		out, err := runIsolated(t, f, map[string]string{"image": other}, "")
		return home, f, out, err
	}

	// A first run that wrote the units and then failed: the service never ran and no settings were saved.
	home, f, out, err := retry(t, func(string, *isoFake) {})
	if err != nil {
		t.Fatalf("a stack that never started may take another image: %v", err)
	}
	if !strings.Contains(out, testDigest) || !strings.Contains(out, other) {
		t.Errorf("the replaced image must be reported with both digests:\n%s", out)
	}
	if serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container"))); !strings.Contains(serve, "@"+other) {
		t.Errorf("the units must carry the new image:\n%s", serve)
	}
	if !f.has("podman pull " + releaseImageRepo + "@" + other) {
		t.Errorf("the new image is the one pulled: %v", f.lines())
	}

	// Anything that shows the stack ran refuses, as an upgrade.
	for name, mutate := range map[string]func(string, *isoFake){
		"service was active": func(_ string, f *isoFake) { f.everActive = true },
		"settings were saved": func(home string, _ *isoFake) {
			if err := SaveSettings(settingsFile(home), isolatedSettings()); err != nil {
				t.Fatal(err)
			}
		},
	} {
		if _, f, _, err := retry(t, mutate); err == nil || !strings.Contains(err.Error(), "--upgrade") || f.has("secret create") {
			t.Fatalf("%s: a stack that ran needs --upgrade, got %v", name, err)
		}
	}
}

func TestInstallIsolatedRefusesAnotherUserOrAnUnexpectedHome(t *testing.T) {
	for name, mutate := range map[string]func(*isoFake){
		"another user":           func(f *isoFake) { f.user = "alice" },
		"home differs from user": func(f *isoFake) { f.passwdHome = filepath.Join(f.home, "elsewhere") },
		"relative home":          func(f *isoFake) { f.home, f.passwdHome = "relative/home", "relative/home" },
	} {
		f := newIsoFake(t.TempDir())
		mutate(f)
		_, err := runIsolated(t, f, nil, "")
		if err == nil || !strings.Contains(err.Error(), "sudo -u suiteward") {
			t.Fatalf("%s: the run must be refused with the command to use, got %v", name, err)
		}
		if f.has("podman") || f.has("secret create") || f.has("start") {
			t.Fatalf("%s: nothing may run before the account is verified: %v", name, f.lines())
		}
	}
}

func TestInstallIsolatedSaysWhenTheOldEnvFileMayGoAccordingToTheServiceState(t *testing.T) {
	for running, want := range map[bool]string{true: "on its next start", false: "now reads"} {
		home := t.TempDir()
		installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, EnvFile: "/etc/suiteward/github.env"})
		if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f := newIsoFake(home)
		f.serviceRunning, f.everActive = running, running
		out, err := runIsolated(t, f, map[string]string{"image": ""}, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, want) || !strings.Contains(out, "sudo rm /etc/suiteward/github.env") {
			t.Errorf("running=%v: the output must say %q and name the file to remove:\n%s", running, want, out)
		}
	}
}

func TestInstallIsolatedAdoptionOfAnEtcEnvFileNeedsTheHostStep(t *testing.T) {
	home := t.TempDir()
	p := isolatedParams()
	p.EnvFile = "/etc/suiteward/github.env"
	installedUnits(t, home, p)
	f := newIsoFake(home)
	_, err := runIsolated(t, f, map[string]string{"image": ""}, "")
	if err == nil || !strings.Contains(err.Error(), "install host") {
		t.Fatalf("a manual install still on the /etc env file needs the host step first: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f = newIsoFake(home)
	lastOut, err := runIsolated(t, f, map[string]string{"image": ""}, "")
	if err != nil {
		t.Fatalf("with the file moved the units are re-rendered on the new path: %v", err)
	}
	serve, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	if !strings.Contains(string(serve), "EnvironmentFile=%h/.config/suiteward/github.env") {
		t.Errorf("the unit must point at the account's env file:\n%s", serve)
	}
	if raw, _ := os.ReadFile(envFile(home)); string(raw) != "SUITEWARD_GITHUB_APP_ID=1\n" {
		t.Errorf("a moved env file must not be overwritten: %q", raw)
	}
	if !strings.Contains(lastOut, "sudo rm /etc/suiteward/github.env") {
		t.Errorf("once the service runs on the new file the old one may go, and the output must say so:\n%s", lastOut)
	}
	if fresh, err := runIsolated(t, newIsoFake(t.TempDir()), nil, ""); err != nil || strings.Contains(fresh, "sudo rm") {
		t.Errorf("a fresh install has no old file to remove: %v\n%s", err, fresh)
	}
}

func TestInstallIsolatedStopsWhenTheGeneratorFindsAProblem(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.failOn[generatorPath] = errors.New("quadlet: unsupported key")
	if _, err := runIsolated(t, f, nil, ""); err == nil || f.has("daemon-reload") || f.has("start") {
		t.Fatalf("a generator error must stop before the reload: %v, %v", err, f.lines())
	}

	f = newIsoFake(t.TempDir())
	f.dryRunSkips = "suiteward-migrate.service"
	_, err := runIsolated(t, f, nil, "")
	if err == nil || !strings.Contains(err.Error(), "suiteward-migrate.service") || f.has("start") {
		t.Fatalf("a unit the generator skipped must be named and stop the run: %v", err)
	}

	// The migrate unit's body still names suiteward-db.service; the database unit itself is gone from the listing.
	f = newIsoFake(t.TempDir())
	f.dryRunSkips = "suiteward-db.service"
	_, err = runIsolated(t, f, nil, "")
	if err == nil || !strings.Contains(err.Error(), "suiteward-db.container") || f.has("start") {
		t.Fatalf("a skipped unit whose name another unit mentions must still be found: %v", err)
	}
}

func TestInstallIsolatedWaitsForTheDatabaseAndGivesUp(t *testing.T) {
	fastHealthWait(t)
	f := newIsoFake(t.TempDir())
	f.health = "starting"
	_, err := runIsolated(t, f, nil, "")
	if err == nil || !strings.Contains(err.Error(), "healthy") {
		t.Fatalf("a database that never gets healthy must stop the run, got %v", err)
	}
	if f.has("psql") || f.has("start suiteward.service") {
		t.Fatalf("neither the role nor the service may follow: %v", f.lines())
	}
}

func TestInstallIsolatedStopsWhenAUnitIsNotActiveAfterTheStart(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.active["suiteward-migrate.service"] = "failed"
	if _, err := runIsolated(t, f, nil, ""); err == nil || !strings.Contains(err.Error(), "suiteward-migrate.service") {
		t.Fatalf("an inactive unit must be named, got %v", err)
	}
}

func TestInstallIsolatedReadsTheAppKeyFromAFileOrStdinAndChecksIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(file, []byte(appKeyPEM(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newIsoFake(t.TempDir())
	if _, err := runIsolated(t, f, map[string]string{"github-app-key": "", "github-app-key-file": file}, ""); err != nil {
		t.Fatal(err)
	}
	if f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Errorf("the key file must reach the secret store")
	}

	f = newIsoFake(t.TempDir())
	if _, err := runIsolated(t, f, map[string]string{"github-app-key": "", "github-app-key-file": "-"}, appKeyPEM(t)); err != nil || f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Fatalf("a key file of - reads standard input: %v", err)
	}

	f = newIsoFake(t.TempDir())
	if _, err := runIsolated(t, f, map[string]string{"github-app-key": ""}, appKeyPEM(t)); err != nil {
		t.Fatalf("the key piped to stdin must work: %v", err)
	}
	if f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Errorf("the stdin key must reach the secret store")
	}

	for name, bad := range map[string]string{"text": "not a key", "certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}))} {
		home := t.TempDir()
		f = newIsoFake(home)
		if _, err := runIsolated(t, f, map[string]string{"github-app-key": bad}, ""); err == nil {
			t.Fatalf("%s: a value that is not a private key must be refused", name)
		}
		if f.has("secret create") || f.has("pull") {
			t.Fatalf("%s: nothing may be created before the key is checked: %v", name, f.lines())
		}
	}
}

func TestInstallIsolatedNeverTakesTheAppKeyAsAnArgument(t *testing.T) {
	f := newIsoFake(t.TempDir())
	term, _ := fakeTerminal(false, "")
	r := newTestResolver(t, map[string]string{"github-app-key": appKeyPEM(t)}, map[string]string{"image": testDigest}, term)
	if err := installIsolated(context.Background(), isolatedSettings(), r, f.deps(), io.Discard); err == nil || f.has("secret create") {
		t.Fatalf("a key on the command line must be refused: %v", err)
	}
}

func TestInstallIsolatedAsksNoAppKeyWhenTheSecretExists(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.secrets[SecretGitHubAppKey] = "existing"
	if _, err := runIsolated(t, f, map[string]string{"github-app-key": ""}, ""); err != nil {
		t.Fatalf("an installed key is not asked for again: %v", err)
	}
	if f.secrets[SecretGitHubAppKey] != "existing" {
		t.Errorf("an installed key must not be replaced")
	}
}

func TestInstallIsolatedWritesTheGitHubEnvFileAllOrNone(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	answers := map[string]string{"github-app-id": "123", "github-installation-id": "456", "github-repositories": "octo/one, octo/two"}
	if _, err := runIsolated(t, f, answers, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(envFile(home))
	if err != nil {
		t.Fatal(err)
	}
	want := "SUITEWARD_GITHUB_APP_ID=123\nSUITEWARD_GITHUB_INSTALLATION_ID=456\nSUITEWARD_GITHUB_REPOSITORIES=octo/one,octo/two\n"
	if string(raw) != want {
		t.Errorf("the env file must hold the three values:\n%s", raw)
	}
	if info, _ := os.Stat(envFile(home)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the env file must be 0600, got %o", info.Mode().Perm())
	}
	if saved, err := LoadSettings(settingsFile(home)); err != nil || saved.GitHub.AppID != 123 || len(saved.GitHub.Repositories) != 2 || saved.GitHub.Owner != "octo" {
		t.Errorf("the GitHub values must reach the settings: %+v, %v", saved.GitHub, err)
	}

	home = t.TempDir()
	f = newIsoFake(home)
	if _, err := runIsolated(t, f, map[string]string{"github-app-id": "123"}, ""); err == nil || f.has("secret create") {
		t.Fatalf("one or two of the three values must be refused before any change: %v", err)
	}
	if _, err := runIsolated(t, newIsoFake(home), map[string]string{"github-app-id": "123", "github-installation-id": "456", "github-repositories": "bad"}, ""); err == nil {
		t.Fatalf("a repository that is not owner/name must be refused")
	}

	// Without values an existing file (for example the one moved from /etc) is left as it is.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runIsolated(t, newIsoFake(home), nil, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(envFile(home)); string(got) != "SUITEWARD_GITHUB_APP_ID=9\n" {
		t.Errorf("an existing env file must stay: %q", got)
	}
}

// assertSecretShapes checks the five secrets the fresh install created: names, shapes and the shared password.
func assertSecretShapes(t *testing.T, f *isoFake) {
	t.Helper()
	for _, name := range []string{SecretDBAuth, SecretDatabaseOwnerURL, SecretMCPAuth, SecretGitHubAppKey, SecretDatabaseURL} {
		if _, ok := f.secrets[name]; !ok {
			t.Errorf("the secret %s was not created: %v", name, f.lines())
		}
	}
	password := f.secrets[SecretDBAuth]
	if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(password) {
		t.Errorf("the database password must be 48 hex characters without a newline: %q", password)
	}
	if want := DatabaseURL("suiteward", password); f.secrets[SecretDatabaseOwnerURL] != want {
		t.Errorf("the owner URL must hold the same password: %q", f.secrets[SecretDatabaseOwnerURL])
	}
	if !regexp.MustCompile(`^postgres://suiteward_app:[0-9a-f]{48}@suiteward-db:5432/suiteward\?sslmode=disable$`).MatchString(f.secrets[SecretDatabaseURL]) {
		t.Errorf("the application URL has the wrong shape: %q", f.secrets[SecretDatabaseURL])
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(f.secrets[SecretMCPAuth]) {
		t.Errorf("the MCP token must be 64 hex characters: %q", f.secrets[SecretMCPAuth])
	}
	if f.secrets[SecretGitHubAppKey] != appKeyStored(t) {
		t.Errorf("the App key secret does not hold the PEM")
	}

}

// assertFilesAndSettings checks the units, the env file and the settings the fresh install left in the account home.
func assertFilesAndSettings(t *testing.T, home string) {
	t.Helper()
	// The rendered units: one digest in both, the isolated tier, the env file under the home of the account.
	for _, unit := range []string{"suiteward-migrate.container", "suiteward.container"} {
		raw := readFileOrFail(t, filepath.Join(unitDir(home), unit))
		if !strings.Contains(string(raw), "\nImage="+releaseImageRepo+"@"+testDigest+"\n") {
			t.Errorf("%s does not carry the digest", unit)
		}
	}
	serve, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	for _, want := range []string{"\nEnvironmentFile=%h/.config/suiteward/github.env\n", "\nEnvironment=SUITEWARD_INSTALLATION_TIER=isolated\n", "\nPublishPort=127.0.0.1:8081:8080\n"} {
		if !strings.Contains(string(serve), want) {
			t.Errorf("suiteward.container lacks %q", want)
		}
	}

	// The env file exists (the unit needs it to start), private to the account, and the settings are saved.
	info, err := os.Stat(envFile(home))
	if err != nil {
		t.Fatalf("the env file must exist: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the env file must be 0600, got %o", info.Mode().Perm())
	}
	saved, err := LoadSettings(settingsFile(home))
	if err != nil || saved.Tier != config.TierIsolated {
		t.Errorf("the settings must be saved as isolated: %+v, %v", saved, err)
	}
}
