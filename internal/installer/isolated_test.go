package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
	tty   []call // the commands that got the real terminal
	ttyAt []int  // how many commands had run when each of them got it
	goos  string
	euid  string

	systemState string // answer of the user manager
	linger      string
	health      string // health of suiteward-db once started
	roleExists  bool
	roleFails   bool              // a statement that creates or alters a role fails, and its error echoes the statement as psql does
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
	dbDown         bool   // suiteward-db.service is inactive until this run starts it
	dbStarted      bool   // suiteward-db.service was started by this run
	migrateDown    bool   // suiteward-migrate.service is inactive until this run starts it
	migrateStarted bool   // suiteward-migrate.service was started or restarted by this run

	dump     string          // what pg_dump prints in the database container
	imported string          // what `podman volume import` was given
	noImage  bool            // no image is in the engine yet, so every pull is real
	objects  map[string]bool // volumes and networks that exist, by name

	listening []int // the TCP ports something listens on, as `ss -ltn` shows them
	restarts  []int // what NRestarts of suiteward.service answers, one per question; the last one repeats, none means 0
	asked     int   // how many times NRestarts was asked
}

func newIsoFake(home string) *isoFake {
	return &isoFake{goos: "linux", euid: "1001", systemState: "running", linger: "yes", health: "healthy", secrets: map[string]string{}, objects: map[string]bool{}, failOn: map[string]error{}, active: map[string]string{}, home: home, user: ServiceAccount, passwdHome: home}
}

var generatedUnits = []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service", "suiteward-network.service", "suiteward-data-volume.service", "suiteward-db-data-volume.service"}

func (f *isoFake) deps() Deps {
	return Deps{
		GOOS:   f.goos,
		Getenv: func(k string) string { return map[string]string{"HOME": f.home}[k] },
		Interactive: func(_ context.Context, name string, args []string) error {
			f.tty = append(f.tty, call{name: name, args: append([]string(nil), args...)})
			f.ttyAt = append(f.ttyAt, len(f.calls))
			return nil
		},
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
			if f.roleFails && strings.Contains(c.line(), "psql") && strings.Contains(c.stdin, " ROLE ") {
				return nil, errors.New("psql: ERROR: LINE 1: " + c.stdin)
			}
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
		if f.inactiveUnit(unit) {
			return []byte("inactive\n"), nil
		}
		return []byte("active\n"), nil
	case c.name == "systemctl" && strings.HasPrefix(line, "--user show --property=LoadState --value "):
		return []byte("loaded\n"), nil
	case c.name == "loginctl":
		return []byte(f.linger + "\n"), nil
	case c.name == generatorPath:
		return f.answerGenerator(), nil
	case c.name == "podman":
		return f.answerPodman(c)
	}
	return f.answerChecks(c)
}

// inactiveUnit says whether the fake reports a unit as inactive: the service until it runs, the database and the migration until
// this run starts them when dbDown or migrateDown.
func (f *isoFake) inactiveUnit(unit string) bool {
	return (unit == "suiteward.service" && !f.serviceRunning && !f.started) ||
		(unit == "suiteward-db.service" && f.dbDown && !f.dbStarted) ||
		(unit == "suiteward-migrate.service" && f.migrateDown && !f.migrateStarted)
}

// answerChecks answers what the install asks to know whether the service can start and stays up: the listeners of the
// machine (ss -ltn) and the restart count of the service. It also records that the database was started.
func (f *isoFake) answerChecks(c call) ([]byte, error) {
	switch line := c.line(); {
	case c.name == "systemctl" && line == "--user start suiteward-db.service":
		f.dbStarted = true
	case c.name == "systemctl" && (line == "--user restart suiteward-migrate.service" || line == "--user start suiteward-migrate.service"):
		f.migrateStarted = true // the migration runs and stays active
	case c.name == "ss" && line == "-ltn":
		out := "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
		for _, port := range f.listening {
			out += "LISTEN 0      4096       127.0.0.1:" + strconv.Itoa(port) + "      0.0.0.0:*\n"
		}
		return []byte(out), nil
	case c.name == "systemctl" && line == "--user show --property=NRestarts --value suiteward.service":
		n := 0
		if len(f.restarts) > 0 {
			n = f.restarts[min(f.asked, len(f.restarts)-1)]
		}
		f.asked++
		return []byte(strconv.Itoa(n) + "\n"), nil
	}
	return nil, nil
}

// answerGenerator is the dry run of the Podman generator.
func (f *isoFake) answerGenerator() []byte {
	var out strings.Builder
	for _, unit := range generatedUnits {
		if unit != f.dryRunSkips {
			out.WriteString("---" + unit + "---\n[Service]\n")
		}
		if unit == "suiteward-migrate.service" { // the units name each other, so a skipped unit's name still shows up
			out.WriteString("Requires=suiteward-db.service\nAfter=suiteward-network.service\n")
		}
	}
	return []byte(out.String())
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
	case strings.HasPrefix(line, "volume export "):
		fields := strings.Fields(line)
		if len(fields) == 5 && fields[3] == "--output" {
			return nil, os.WriteFile(fields[4], []byte("TARBYTES"), 0o644) //nolint:gosec // the engine writes it world-readable
		}
	case strings.HasPrefix(line, "volume import "):
		fields := strings.Fields(line)
		if len(fields) == 4 {
			raw, err := os.ReadFile(fields[3])
			f.imported = string(raw)
			return nil, err
		}
	case strings.HasPrefix(line, "exec -i suiteward-db pg_dump"):
		return []byte(f.dump), nil
	case strings.HasPrefix(line, "image inspect "):
		if f.noImage {
			return nil, errors.New("no such image")
		}
	case strings.HasPrefix(line, "volume ls --filter name="), strings.HasPrefix(line, "network ls --filter name="):
		if name := strings.Fields(line)[3][len("name="):]; f.objects[name] {
			return []byte(name + "\n"), nil
		}
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

// runIsolated runs installIsolated through the GitHub flow of isoGitHub (an App that exists), with the image given as an answer,
// unless the test overrides the answers; an empty value removes one.
func runIsolated(t *testing.T, f *isoFake, answers map[string]string, stdin string) (string, error) {
	t.Helper()
	g := isoGitHubOn(t, f)
	g.answers, g.stdin = answers, stdin
	err := g.run()
	return g.out.String(), err
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
	f.noImage = true // the images are pulled
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
	role := f.index("exec -i suiteward-db psql -X -q -t -A -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f -", health) // the look for the role
	for role >= 0 && !strings.Contains(f.calls[role].stdin, "CREATE ROLE") {
		role = f.index("exec -i suiteward-db psql", role+1)
	}
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
	re := regexp.MustCompile(`(?s)^SET log_statement = 'none'; SET log_min_error_statement = 'panic'; SET log_min_duration_statement = -1; CREATE ROLE suiteward_app LOGIN PASSWORD '([0-9a-f]{48})';$`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("the role SQL has the wrong shape: %q", sql)
	}
	if !strings.Contains(f.secrets[SecretDatabaseURL], ":"+m[1]+"@") {
		t.Errorf("the application URL must carry the password of the role")
	}

	assertFilesAndSettings(t, home)

	for _, want := range []string{"suiteward enroll --tier isolated", "SHA256:", "suiteward doctor"} {
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
	f.noImage = true
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
	if strings.Contains(out, "you asked for") {
		t.Errorf("no port was asked for, so the installed ones are adopted without saying that something was asked:\n%s", out)
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
		f.noImage = true
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
		installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, EnvFile: "/etc/suiteward/github.env", NoExecutor: true}) // a stack of a release before the executor
		if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envFile(home), []byte(fullEnv), 0o600); err != nil {
			t.Fatal(err)
		}
		f := newIsoFake(home)
		f.serviceRunning, f.everActive = running, running
		out, err := runIsolated(t, f, addAnswers(withoutGitHub(f), "image", ""), "")
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
	_, err := runIsolated(t, f, addAnswers(withoutGitHub(f), "image", ""), "")
	if err == nil || !strings.Contains(err.Error(), "install host") {
		t.Fatalf("a manual install still on the /etc env file needs the host step first: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte(fullEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	f = newIsoFake(home)
	lastOut, err := runIsolated(t, f, addAnswers(withoutGitHub(f), "image", ""), "")
	if err != nil {
		t.Fatalf("with the file moved the units are re-rendered on the new path: %v", err)
	}
	serve, _ := os.ReadFile(filepath.Join(unitDir(home), "suiteward.container"))
	if !strings.Contains(string(serve), "EnvironmentFile=%h/.config/suiteward/github.env") {
		t.Errorf("the unit must point at the account's env file:\n%s", serve)
	}
	if raw, _ := os.ReadFile(envFile(home)); string(raw) != fullEnv {
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

func TestInstallIsolatedKeepsAnInstalledAppKey(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.secrets[SecretGitHubAppKey] = "existing"
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatalf("an installed key is not stored again: %v", err)
	}
	if f.secrets[SecretGitHubAppKey] != "existing" || f.has("secret create "+SecretGitHubAppKey) {
		t.Errorf("an installed key must not be replaced")
	}
}

func TestInstallIsolatedWritesTheGitHubEnvFileFromTheFlowAndLeavesAnExistingOneAlone(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	want := "SUITEWARD_GITHUB_APP_ID=123\nSUITEWARD_GITHUB_INSTALLATION_ID=456\nSUITEWARD_GITHUB_REPOSITORIES=octo/one,octo/two\n"
	if got := string(readFileOrFail(t, envFile(home))); got != want {
		t.Errorf("the env file must hold the three values:\n%s", got)
	}
	if info, _ := os.Stat(envFile(home)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the env file must be 0600, got %o", info.Mode().Perm())
	}
	if saved, err := LoadSettings(settingsFile(home)); err != nil || saved.GitHub.AppID != 123 || len(saved.GitHub.Repositories) != 2 || saved.GitHub.Owner != "octo" {
		t.Errorf("the GitHub values must reach the settings: %+v, %v", saved.GitHub, err)
	}

	// A run that does not go through the flow leaves an existing file (for example the one moved from /etc) as it is.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte(fullEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	f = newIsoFake(home)
	if _, err := runIsolated(t, f, withoutGitHub(f), ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(envFile(home)); string(got) != fullEnv {
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
	for _, want := range []string{"\nEnvironmentFile=%h/.config/suiteward/github.env\n", "\nEnvironment=SUITEWARD_INSTALLATION_TIER=isolated\n", "\nPublishPort=127.0.0.1:27081:8080\n"} {
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

// addAnswers puts one more answer into a map of answers.
func addAnswers(m map[string]string, key, value string) map[string]string {
	m[key] = value
	return m
}

// fullEnv is a GitHub env file that holds all three values.
const fullEnv = "SUITEWARD_GITHUB_APP_ID=1\nSUITEWARD_GITHUB_INSTALLATION_ID=2\nSUITEWARD_GITHUB_REPOSITORIES=octo/one\n"

// ---- the isolated install of v0.3.0-alpha.2: its flags, GitHub off, a stable service and free ports ----

// installIsolatedArgs runs `suiteward install --tier isolated` on the machine f with the GitHub flow faked, as the command line
// does: the answers of the flow are flags, and args are the flags under test.
func installIsolatedArgs(t *testing.T, f *isoFake, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	env := Env{
		Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {},
		ResolveGitHub: func(context.Context, githubEnv) (GitHubResult, error) {
			return GitHubResult{
				AppID: 123, InstallationID: 456, Account: "octo", AccountType: "User", OwnerLogin: "alice",
				Repositories: []string{"octo/one", "octo/two"}, TestChecks: map[string]string{"octo/one": "CI / test", "octo/two": "CI / test"},
				PrivateKey: []byte(appKeyPEM(t)),
			}, nil
		},
		HTTPGet: func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
	}
	base := []string{"--tier", "isolated", "--settings", settingsFile(f.home), "--image", testDigest,
		"--github-app-id", "123", "--github-installation-id", "456", "--github-repositories", "octo/one,octo/two", "--github-owner", "alice"}
	err := Install(context.Background(), append(base, args...), env)
	return out.String(), err
}

func TestInstallIsolatedHonorsThePortsAndTheJobTimeoutItIsAsked(t *testing.T) {
	check := func(t *testing.T, home string) {
		t.Helper()
		serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
		for _, want := range []string{"\nPublishPort=127.0.0.1:18082:8080\n", "\nPublishPort=127.0.0.1:18092:8090\n", "\nEnvironment=SUITEWARD_JOB_TIMEOUT=5m\n"} {
			if !strings.Contains(serve, want) {
				t.Errorf("the unit lacks %q:\n%s", want, serve)
			}
		}
		if saved, err := LoadSettings(settingsFile(home)); err != nil || saved.HTTPPort != 18082 || saved.MCPPort != 18092 || saved.JobTimeout != "5m" {
			t.Errorf("the settings keep %+v, %v", saved, err)
		}
	}

	t.Run("flags", func(t *testing.T) {
		f := newIsoFake(t.TempDir())
		if _, err := installIsolatedArgs(t, f, "--http-port", "18082", "--mcp-port", "18092", "--job-timeout", "5m"); err != nil {
			t.Fatal(err)
		}
		check(t, f.home)
	})
	t.Run("answers file", func(t *testing.T) {
		f := newIsoFake(t.TempDir())
		file := filepath.Join(t.TempDir(), "answers.yaml")
		if err := os.WriteFile(file, []byte("http-port: 18082\nmcp-port: 18092\njob-timeout: 5m\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := installIsolatedArgs(t, f, "--answers", file); err != nil {
			t.Fatal(err)
		}
		check(t, f.home)
	})
	t.Run("a flag wins over the answers file", func(t *testing.T) {
		f := newIsoFake(t.TempDir())
		file := filepath.Join(t.TempDir(), "answers.yaml")
		if err := os.WriteFile(file, []byte("http-port: 9999\nmcp-port: 18092\njob-timeout: 5m\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := installIsolatedArgs(t, f, "--answers", file, "--http-port", "18082"); err != nil {
			t.Fatal(err)
		}
		check(t, f.home)
	})
}

func TestInstallIsolatedRefusesInvalidPortsAndJobTimeoutBeforeAnyChange(t *testing.T) {
	for name, args := range map[string][]string{
		"port out of range": {"--http-port", "70000"},
		"port not a number": {"--mcp-port", "abc"},
		"the same port":     {"--http-port", "27091"}, // the default MCP port
		"job timeout":       {"--job-timeout", "soon"},
	} {
		f := newIsoFake(t.TempDir())
		f.noImage = true
		if _, err := installIsolatedArgs(t, f, args...); err == nil {
			t.Fatalf("%s: the install must be refused", name)
		}
		if f.has("secret create") || f.has("pull") || f.has("start") || f.has("daemon-reload") || exists(filepath.Join(f.home, ".config")) {
			t.Errorf("%s: something was changed: %v", name, f.lines())
		}
	}
}

func TestInstallIsolatedWithoutGitHubRunsWithoutTheKeyLines(t *testing.T) {
	keyLines := []string{"Secret=suiteward-github-app-key", "SUITEWARD_GITHUB_APP_KEY_FILE"}
	for name, answers := range map[string]map[string]string{
		"none":   {"github-app-id": "none"},
		"no":     {"github-app-id": "", "github": "no"},
		"absent": {"github-app-id": "", "github-installation-id": "", "github-repositories": "", "github-owner": ""},
	} {
		g := newIsoGitHub(t) // no key secret exists: GitHub off must not need one
		maps.Copy(g.answers, answers)
		if err := g.run(); err != nil {
			t.Fatalf("%s: an install without GitHub must work without a key secret: %v", name, err)
		}
		serve := string(readFileOrFail(t, filepath.Join(unitDir(g.home), "suiteward.container")))
		for _, line := range keyLines {
			if strings.Contains(serve, line) {
				t.Errorf("%s: the unit mounts a key that does not exist (%s):\n%s", name, line, serve)
			}
		}
		if g.f.has("secret create " + SecretGitHubAppKey) {
			t.Errorf("%s: a key secret was created without a key", name)
		}
		if !strings.Contains(g.out.String(), "GitHub is not configured") {
			t.Errorf("%s: the output must say GitHub is off:\n%s", name, g.out.String())
		}
		if raw := readFileOrFail(t, envFile(g.home)); len(raw) != 0 {
			t.Errorf("%s: the env file must be empty: %q", name, raw)
		}
		if saved, err := LoadSettings(settingsFile(g.home)); err != nil || saved.GitHub.AppID != 0 {
			t.Errorf("%s: the settings keep %+v, %v", name, saved, err)
		}
	}

	// With an App the unit mounts the key and reads its name from the environment, as before.
	g := newIsoGitHub(t)
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	serve := string(readFileOrFail(t, filepath.Join(unitDir(g.home), "suiteward.container")))
	for _, line := range keyLines {
		if !strings.Contains(serve, line) {
			t.Errorf("with an App the unit lacks %s:\n%s", line, serve)
		}
	}
}

func TestInstallIsolatedRefusesToDropTheAppOfTheEnvFileBeforeAnyChange(t *testing.T) {
	// --github-app-id none does not empty an env file that holds an App, and the service reads that file: without the key lines
	// it would stop at startup, so the run says so instead.
	g := newIsoGitHub(t)
	handInstalled(t, g, handMadeEnv)
	g.answers["github-app-id"] = "none"
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), envFile(g.home)) {
		t.Fatalf("the run must name the env file that still holds an App, got %v", err)
	}
	if g.f.has("secret create") || g.f.has("start") || g.f.has("daemon-reload") || exists(unitDir(g.home)) {
		t.Fatalf("nothing may change: %v", g.f.lines())
	}
	if got := string(readFileOrFail(t, envFile(g.home))); got != handMadeEnv {
		t.Errorf("the env file was rewritten: %q", got)
	}
}

func TestInstallIsolatedSaysRunningOnlyOnceTheServiceIsStable(t *testing.T) {
	journal := "journalctl --user -u suiteward.service"
	// ready answers /readyz from a script: true is a 200, false a 503; once the script ends it answers 200.
	ready := func(g *isoGitHub, script ...bool) *int {
		polls := 0
		g.status = func(_ context.Context, address string) ([]byte, error) {
			if !strings.HasSuffix(address, "/readyz") {
				return []byte(`{"version":"v0.2.0"}`), nil
			}
			ok := true
			if polls < len(script) {
				ok = script[polls]
			}
			polls++
			if !ok {
				return nil, errors.New("HTTP 503")
			}
			return []byte("ok"), nil
		}
		return &polls
	}
	notStable := func(t *testing.T, g *isoGitHub, want string) {
		t.Helper()
		err := g.run()
		if err == nil || !strings.Contains(err.Error(), journal) || !strings.Contains(err.Error(), want) {
			t.Fatalf("the install must fail naming %q and %q, got %v", want, journal, err)
		}
		if strings.Contains(g.out.String(), "is running") {
			t.Errorf("a service that is not stable must not be called running:\n%s", g.out.String())
		}
		if _, statErr := os.Stat(settingsFile(g.home)); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("the settings must stay unsaved while the service is not stable: %v", statErr)
		}
	}

	t.Run("readyz never answers", func(t *testing.T) {
		g := newIsoGitHub(t)
		ready(g, slices.Repeat([]bool{false}, 500)...)
		notStable(t, g, "/readyz")
	})
	t.Run("the service restarts while it is polled", func(t *testing.T) {
		g := newIsoGitHub(t)
		ready(g)
		g.f.restarts = []int{0, 1} // one restart after the first question
		notStable(t, g, "restart")
	})
	t.Run("a failed poll starts the count again", func(t *testing.T) {
		g := newIsoGitHub(t)
		polls := ready(g, true, true, false)
		if err := g.run(); err != nil {
			t.Fatal(err)
		}
		if *polls != 7 {
			t.Errorf("polled %d times, want 7: two answers, a failure, then four in a row", *polls)
		}
		if !strings.Contains(g.out.String(), "is running") {
			t.Errorf("the install must say it runs:\n%s", g.out.String())
		}
	})
	t.Run("four answers in a row", func(t *testing.T) {
		// Four polls two seconds apart span more than RestartSec=5 of the unit, so a service that crashes right after it answers
		// has restarted, or failed a poll, before the install says it runs.
		g := newIsoGitHub(t)
		g.answers["http-port"] = "18082"
		polls := ready(g)
		if err := g.run(); err != nil {
			t.Fatal(err)
		}
		if *polls != 4 {
			t.Errorf("polled %d times, want 4", *polls)
		}
		if !slices.Contains(g.asked, "http://127.0.0.1:18082/readyz") {
			t.Errorf("/readyz is read on the HTTP port of the install: %v", g.asked)
		}
		if !strings.Contains(g.out.String(), "is running") {
			t.Errorf("the install must say it runs:\n%s", g.out.String())
		}
	})
}

func TestInstallIsolatedRefusesAPortSomethingElseListensOn(t *testing.T) {
	for name, tc := range map[string]struct {
		port int
		flag string
	}{"http": {27081, "--http-port"}, "mcp": {27091, "--mcp-port"}} {
		f := newIsoFake(t.TempDir())
		f.noImage, f.listening = true, []int{tc.port}
		_, err := runIsolated(t, f, nil, "")
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(tc.port)) || !strings.Contains(err.Error(), tc.flag) {
			t.Fatalf("%s: the run must name the port and %s, got %v", name, tc.flag, err)
		}
		if f.has("secret create") || f.has("pull") || f.has("start") || f.has("daemon-reload") || exists(filepath.Join(f.home, ".config")) {
			t.Errorf("%s: something was changed: %v", name, f.lines())
		}
	}
}

// installedStackUnits installs the stack the next tests re-run over: ports 18081 and 18091, a job timeout of 3m.
func installedStackUnits(t *testing.T, home string) {
	t.Helper()
	installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: 18081, MCPPort: 18091, JobTimeout: "3m", EnvFile: DefaultEnvFile, NoExecutor: true}) // a stack of a release before the executor, which the host has no token for
}

func TestInstallIsolatedChecksEveryPortButTheOnesOfTheRunningStack(t *testing.T) {
	for name, tc := range map[string]struct {
		running   bool
		answers   map[string]string
		listening []int
		refuses   string // the port and the flag the refusal names; empty when the install goes on
	}{
		"running, nothing given: its own ports":              {true, nil, []int{18081, 18091}, ""},
		"running, another port given: the old ones go":       {true, map[string]string{"http-port": "18082"}, []int{18081, 18091}, ""},
		"running, a given port that something else holds":    {true, map[string]string{"http-port": "18082"}, []int{18081, 18091, 18082}, "18082 --http-port"},
		"running, the two ports swapped: both are its own":   {true, map[string]string{"http-port": "18091", "mcp-port": "18081"}, []int{18081, 18091}, ""},
		"not running: a listener is not the stack's":         {false, nil, []int{18081}, "18081 --http-port"},
		"not running: the MCP port is checked as well":       {false, nil, []int{18091}, "18091 --mcp-port"},
		"not running, other ports given: the old ones go":    {false, map[string]string{"http-port": "18082", "mcp-port": "18092"}, []int{18081, 18091}, ""},
		"not running, a given port that something else owns": {false, map[string]string{"mcp-port": "18092"}, []int{18092}, "18092 --mcp-port"},
	} {
		home := t.TempDir()
		installedStackUnits(t, home)
		f := newIsoFake(home)
		f.noImage, f.serviceRunning, f.everActive, f.listening = true, tc.running, tc.running, tc.listening
		_, err := runIsolated(t, f, tc.answers, "")
		if tc.refuses == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		port, flag, _ := strings.Cut(tc.refuses, " ")
		if err == nil || !strings.Contains(err.Error(), port) || !strings.Contains(err.Error(), flag) {
			t.Errorf("%s: the run must name %s and %s, got %v", name, port, flag, err)
		}
		if f.has("secret create") || f.has("pull") || f.has("daemon-reload") || f.has("start") || f.has("restart") {
			t.Errorf("%s: something was changed: %v", name, f.lines())
		}
	}
}

// assertRerunTookTheGivenValues checks a re-run over installedStackUnits that was given http-port 18082 and job-timeout 9m: the
// units and the settings carry them, the MCP port stays, and the output says what changed.
func assertRerunTookTheGivenValues(t *testing.T, home, out string) {
	t.Helper()
	serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
	for _, want := range []string{"\nPublishPort=127.0.0.1:18082:8080\n", "\nPublishPort=127.0.0.1:18091:8090\n", "\nEnvironment=SUITEWARD_JOB_TIMEOUT=9m\n"} {
		if !strings.Contains(serve, want) {
			t.Errorf("the unit lacks %q (the MCP port was not given, so it stays):\n%s", want, serve)
		}
	}
	if saved, err := LoadSettings(settingsFile(home)); err != nil || saved.HTTPPort != 18082 || saved.MCPPort != 18091 || saved.JobTimeout != "9m" {
		t.Errorf("the settings keep %+v, %v", saved, err)
	}
	for _, want := range []string{"18081", "18082", `"3m"`, `"9m"`, "Changing"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output must say what changes (%q):\n%s", want, out)
		}
	}
	if strings.Contains(out, "Changing the MCP port") {
		t.Errorf("the MCP port does not change:\n%s", out)
	}
}

var rerunGiven = map[string]string{"http-port": "18082", "job-timeout": "9m"}

func TestInstallIsolatedRerunTakesTheGivenPortsAndJobTimeoutOverTheInstalledOnes(t *testing.T) {
	// A stack whose first start failed: it never ran, so the service is started, not restarted.
	home := t.TempDir()
	installedStackUnits(t, home)
	g := isoGitHubOn(t, newIsoFake(home))
	g.answers = rerunGiven
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	assertRerunTookTheGivenValues(t, home, g.out.String())
	if g.f.has("restart suiteward.service") {
		t.Errorf("a service that was not running is started, not restarted: %v", g.f.lines())
	}
}

func TestInstallIsolatedRerunRestartsARunningServiceToTakeTheGivenValues(t *testing.T) {
	home := t.TempDir()
	installedStackUnits(t, home)
	f := newIsoFake(home)
	f.serviceRunning, f.everActive, f.listening = true, true, []int{18081, 18091}
	g := isoGitHubOn(t, f)
	g.answers = rerunGiven
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	assertRerunTookTheGivenValues(t, home, g.out.String())
	if !f.has("systemctl --user restart suiteward.service") {
		t.Errorf("a running service reads its units only at its next start: %v", f.lines())
	}
	if !slices.Contains(g.asked, "http://127.0.0.1:18082/readyz") {
		t.Errorf("/readyz is read on the new port: %v", g.asked)
	}
}

// flowAppEnvFile is the env file of an install with the App the fake GitHub flow returns: what writeEnvFile writes for it.
const flowAppEnvFile = "SUITEWARD_GITHUB_APP_ID=123\nSUITEWARD_GITHUB_INSTALLATION_ID=456\nSUITEWARD_GITHUB_REPOSITORIES=octo/one,octo/two\n"

// writeAccountEnvFile writes the GitHub env file the service of an earlier install reads.
func writeAccountEnvFile(t *testing.T, home, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(envFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIsolatedRerunWithNothingGivenKeepsWhatIsInstalledAndLeavesARunningServiceAlone(t *testing.T) {
	home := t.TempDir()
	installedStackUnits(t, home)
	writeAccountEnvFile(t, home, flowAppEnvFile) // the service of a stack with GitHub reads it, and the key secret is there
	f := newIsoFake(home)
	f.secrets[SecretGitHubAppKey] = "existing"
	f.serviceRunning, f.everActive, f.listening = true, true, []int{18081, 18091}
	out, err := runIsolated(t, f, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
	for _, want := range []string{"\nPublishPort=127.0.0.1:18081:8080\n", "\nPublishPort=127.0.0.1:18091:8090\n", "\nEnvironment=SUITEWARD_JOB_TIMEOUT=3m\n"} {
		if !strings.Contains(serve, want) {
			t.Errorf("the installed value %q must stay:\n%s", want, serve)
		}
	}
	if f.has("restart suiteward.service") || strings.Contains(out, "Changing") {
		t.Errorf("nothing changes, so nothing restarts:\n%s\n%v", out, f.lines())
	}
}

// A running service reads its units, its env file and its key secret only when it starts: a re-run that changes any of them over
// an active service restarts it, before setup enrolls the owner on it; a re-run that changes none leaves it alone.
func TestInstallIsolatedRerunRestartsARunningServiceWhenWhatItReadsChanges(t *testing.T) {
	off := map[string]string{"github-app-id": "none", "github-installation-id": "", "github-repositories": "", "github-owner": ""}
	for name, tc := range map[string]struct {
		noGitHub  bool              // the installed service unit has no key lines
		env       string            // the env file of the stack
		keySecret bool              // the key secret exists
		answers   map[string]string // over the answers of newIsoGitHub: the App 123
		restarts  bool
	}{
		"GitHub is turned on over a stack without it": {true, "", false, nil, true},
		"GitHub is turned off over a stack with it":   {false, "", true, off, true},
		"another App":                    {false, handMadeEnv, true, nil, true},
		"the key secret is stored again": {false, flowAppEnvFile, false, nil, true},
		"nothing changed":                {false, flowAppEnvFile, true, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			terminalForSetup(t)
			home := t.TempDir()
			installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, EnvFile: DefaultEnvFile, NoGitHub: tc.noGitHub, NoExecutor: true})
			writeAccountEnvFile(t, home, tc.env)
			f := newIsoFake(home)
			f.serviceRunning, f.everActive = true, true
			if tc.keySecret {
				f.secrets[SecretGitHubAppKey] = "existing"
			}
			g := isoGitHubOn(t, f)
			maps.Copy(g.answers, tc.answers)
			if err := g.run(); err != nil {
				t.Fatalf("%v\n%s", err, g.out.String())
			}
			restart := f.index("systemctl --user restart suiteward.service", 0)
			switch {
			case tc.restarts && restart < 0:
				t.Fatalf("the service runs with what it read at its last start: it must be restarted:\n%s", strings.Join(f.lines(), "\n"))
			case !tc.restarts && restart >= 0:
				t.Fatalf("nothing the service reads changed, so it must not be restarted:\n%s", strings.Join(f.lines(), "\n"))
			case tc.restarts && tc.answers == nil && len(f.ttyAt) != 1: // with an App, setup runs once; without one it does not run
				t.Fatalf("setup must run once on the terminal after the restart, it ran %d times", len(f.ttyAt))
			case tc.restarts && len(f.ttyAt) > 0 && restart >= f.ttyAt[0]:
				t.Errorf("setup enrolled the owner on the old service: the restart is call %d, setup came after %d calls", restart, f.ttyAt[0])
			}
		})
	}
}

func TestInstallIsolatedCarriesOnWhenSSIsNotInstalled(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.failOn["ss -ltn"] = fmt.Errorf("ss -ltn: %w", exec.ErrNotFound)
	out, err := runIsolated(t, f, nil, "")
	if err != nil {
		t.Fatalf("a check that cannot run is reported, and the readiness wait still catches a port in use: %v", err)
	}
	if !strings.Contains(out, "could not be checked") {
		t.Errorf("the output must say the ports were not checked:\n%s", out)
	}
}

func TestInstallIsolatedStopsWhenTheListenersCannotBeListedForAnotherReason(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.noImage = true
	f.failOn["ss -ltn"] = errInjected // a non-zero exit, or a context that was cancelled
	if _, err := runIsolated(t, f, nil, ""); !errors.Is(err, errInjected) {
		t.Fatalf("a check that failed is not a check that passed: %v", err)
	}
	if f.has("secret create") || f.has("pull") || f.has("daemon-reload") || f.has("start") {
		t.Errorf("something was changed: %v", f.lines())
	}
}

func TestInstallIsolatedNamesWhereAnInvalidValueCameFrom(t *testing.T) {
	answers := func(t *testing.T, text string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "answers.yaml")
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"a flag that is not a number":     {[]string{"--http-port", "abc"}, []string{"--http-port", "abc"}},
		"a flag out of range":             {[]string{"--mcp-port", "70000"}, []string{"--mcp-port", "70000"}},
		"a key of the answers file":       {[]string{"--answers", answers(t, "http-port: abc\n")}, []string{`"http-port" in the answers file`, "abc"}},
		"a value of the answers file":     {[]string{"--answers", answers(t, "mcp-port: 70000\n")}, []string{`"mcp-port" in the answers file`, "70000"}},
		"a job timeout of the flag":       {[]string{"--job-timeout", "soon"}, []string{"--job-timeout", "soon"}},
		"a job timeout of the answers":    {[]string{"--answers", answers(t, "job-timeout: soon\n")}, []string{`"job-timeout" in the answers file`, "soon"}},
		"the flag of two the file offers": {[]string{"--answers", answers(t, "http-port: 9\nmcp-port: 70000\n"), "--http-port", "18082"}, []string{`"mcp-port" in the answers file`}},
	} {
		_, err := installIsolatedArgs(t, newIsoFake(t.TempDir()), tc.args...)
		if err == nil {
			t.Errorf("%s: the install must be refused", name)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error lacks %q: %v", name, want, err)
			}
		}
	}
}

func TestInstallIsolatedNamesTheEnvFileWhenItsAppHasNoKey(t *testing.T) {
	// The App of the env file, whose key secret is gone: the way to run without GitHub starts in that file, which the service
	// reads, and --github-app-id none alone is refused while the file holds an App.
	g := newIsoGitHub(t)
	handInstalled(t, g, handMadeEnv)
	delete(g.f.secrets, SecretGitHubAppKey)
	inner := g.flow
	g.flow = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		res, err := inner(ctx, e)
		res.PrivateKey = nil
		return res, err
	}
	err := g.run()
	if err == nil || !strings.Contains(err.Error(), envFile(g.home)) || !strings.Contains(err.Error(), "SUITEWARD_GITHUB_") {
		t.Fatalf("the error must name the env file and its SUITEWARD_GITHUB_ lines: %v", err)
	}
	if g.f.has("secret create") || g.f.has("start") || g.f.has("daemon-reload") {
		t.Errorf("nothing may change: %v", g.f.lines())
	}
}

// The network service of the stack is a oneshot that stays active: after a purge removed the Podman network, a database started
// while that service is still "active" exits 125 (network not found), because systemd never runs `podman network create` again.
// With the database, the migration and the service all down nothing that needs the network runs, so the install restarts the
// network service first, which creates it again (--ignore: the network may exist).
func TestInstallIsolatedRestartsTheNetworkServiceBeforeItStartsAStoppedStack(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.dbDown, f.migrateDown = true, true // and the service has not run
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	reload := f.index("systemctl --user daemon-reload", 0)
	network := f.index("systemctl --user restart suiteward-network.service", reload)
	start := f.index("systemctl --user start suiteward-db.service", reload)
	if reload < 0 || network < 0 || start < 0 || network > start {
		t.Fatalf("the network service must be restarted after the reload and before the database starts:\n%s", strings.Join(f.lines(), "\n"))
	}
}

// Every container Requires= the network service, so restarting it restarts what runs: a database that crashed while the migration
// or the service run is no reason to touch it.
func TestInstallIsolatedRestartsTheNetworkServiceOnlyWhenNothingOfTheStackRuns(t *testing.T) {
	for name, tc := range map[string]struct {
		dbDown, migrateDown, serviceRuns bool
		restarts                         bool
	}{
		"the database, the migration and the service are down": {true, true, false, true},
		"the database is down and the service runs":            {true, false, true, false},
		"the database is down and the migration runs":          {true, false, false, false},
		"the database runs":                                    {false, true, false, false},
		"all of them run":                                      {false, false, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newIsoFake(t.TempDir())
			f.dbDown, f.migrateDown, f.serviceRunning, f.everActive = tc.dbDown, tc.migrateDown, tc.serviceRuns, tc.serviceRuns
			if _, err := runIsolated(t, f, nil, ""); err != nil {
				t.Fatal(err)
			}
			if restarted := f.has("restart suiteward-network.service"); restarted != tc.restarts {
				t.Errorf("network service restarted = %v, want %v:\n%s", restarted, tc.restarts, strings.Join(f.lines(), "\n"))
			}
		})
	}
}

func TestInstallIsolatedLeavesTheNetworkServiceAloneWhenTheDatabaseRuns(t *testing.T) {
	home := t.TempDir()
	installedStackUnits(t, home)
	writeAccountEnvFile(t, home, flowAppEnvFile)
	f := newIsoFake(home)
	f.secrets[SecretGitHubAppKey] = "existing"
	f.serviceRunning, f.everActive, f.listening = true, true, []int{18081, 18091} // the whole stack runs
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	if f.has("suiteward-network.service") {
		t.Errorf("the network service of a stack that runs was restarted:\n%s", strings.Join(f.lines(), "\n"))
	}
}
