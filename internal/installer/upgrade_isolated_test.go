package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// isoUpgrade is an isolated host that runs an older release, with a service that answers /status.
type isoUpgrade struct {
	t       *testing.T
	f       *isoFake
	home    string
	backups string
	port    int
	out     bytes.Buffer
}

func newIsoUpgrade(t *testing.T, edit func(p *UnitParams)) *isoUpgrade {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, statusBody)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	f := newIsoFake(home)
	f.dump, f.serviceRunning, f.everActive = dumpText, true, true
	p := UnitParams{Digest: oldDigest, Tier: config.TierIsolated, HTTPPort: port, MCPPort: port + 1, JobTimeout: "3m", EnvFile: DefaultEnvFile}
	if edit != nil {
		edit(&p)
	}
	installedUnits(t, home, p)
	s := isolatedSettings()
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = port, port+1, "3m", oldImage
	if err := SaveSettings(settingsFile(home), s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(home), []byte("SUITEWARD_GITHUB_APP_ID=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &isoUpgrade{t: t, f: f, home: home, backups: filepath.Join(home, "backups"), port: port}
}

func (u *isoUpgrade) env() Env {
	return Env{
		Deps:     u.f.deps(),
		Term:     Terminal{In: strings.NewReader(""), Out: &u.out},
		PortFree: func(int) error { return nil },
		Sleep:    func(time.Duration) {},
	}
}

func (u *isoUpgrade) upgrade(args ...string) error {
	u.t.Helper()
	fastHealthWait(u.t)
	return Install(context.Background(), append([]string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups}, args...), u.env())
}

func (u *isoUpgrade) uninstall(args ...string) error {
	u.t.Helper()
	return Uninstall(context.Background(), append([]string{"--tier", "isolated", "--settings", settingsFile(u.home)}, args...), u.env())
}

func (u *isoUpgrade) unit(name string) string {
	return readFile(u.t, filepath.Join(unitDir(u.home), name))
}

// untouched fails when the run changed anything: no service action, no pull, no unit written.
func (u *isoUpgrade) untouched(unitsBefore map[string]string) {
	u.t.Helper()
	for _, c := range u.f.lines() {
		for _, change := range []string{"systemctl --user stop", "systemctl --user restart", "systemctl --user start", "daemon-reload", "podman pull", "secret rm", "volume rm", "network rm"} {
			if strings.Contains(c, change) {
				u.t.Fatalf("a change was made: %s", c)
			}
		}
	}
	for name, want := range unitsBefore {
		if got := u.unit(name); got != want {
			u.t.Errorf("%s changed", name)
		}
	}
}

func (u *isoUpgrade) units() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(unitDir(u.home))
	if err != nil {
		u.t.Fatal(err)
	}
	for _, e := range entries {
		out[e.Name()] = u.unit(e.Name())
	}
	return out
}

func TestUpgradeIsolatedRunsEveryStepInOrder(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.noImage = true
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatal(err)
	}
	at := 0
	for _, marker := range []string{
		"podman exec -i suiteward-db pg_dump -U suiteward -d suiteward",
		"podman volume export suiteward-data --output",
		"podman pull " + releaseImageRepo + "@" + testDigest,
		"systemctl --user stop suiteward.service",
		generatorPath + " --user --dryrun",
		"systemctl --user daemon-reload",
		"systemctl --user restart suiteward-migrate.service",
		"systemctl --user start suiteward.service",
		"--property=ActiveState --value suiteward.service",
	} {
		i := u.f.index(marker, at)
		if i < 0 {
			t.Fatalf("no %q after call %d:\n%s", marker, at, strings.Join(u.f.lines(), "\n"))
		}
		at = i + 1
	}
	if u.f.has("restart suiteward-db.service") {
		t.Errorf("the database was restarted although its pin did not change")
	}
	for _, unit := range []string{"suiteward-db.service", "suiteward-migrate.service", "suiteward.service"} {
		if !u.f.has("--property=ActiveState --value " + unit) {
			t.Errorf("%s is not checked", unit)
		}
	}
	for _, name := range []string{"suiteward.container", "suiteward-migrate.container"} {
		if text := u.unit(name); !strings.Contains(text, "Image="+releaseImageRepo+"@"+testDigest+"\n") || strings.Contains(text, oldDigest) {
			t.Errorf("%s was not moved to the new digest:\n%s", name, text)
		}
	}
	if serve := u.unit("suiteward.container"); !strings.Contains(serve, "127.0.0.1:"+strconv.Itoa(u.port)+":8080") || !strings.Contains(serve, "SUITEWARD_JOB_TIMEOUT=3m") {
		t.Errorf("the local fields were not carried:\n%s", serve)
	}
	if got, err := LoadSettings(settingsFile(u.home)); err != nil || got.Image != testImage || got.JobTimeout != "3m" {
		t.Errorf("saved settings %+v, %v", got, err)
	}
	out := u.out.String()
	for _, want := range []string{oldDigest, testDigest, u.backups, "suiteward doctor", "restore"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HUNTER2-TOTP") {
		t.Errorf("the dump reached the output")
	}
	u.f.secretValuesNotInArgs(t)
}

func TestUpgradeIsolatedKeepsABackupAndTheOldUnits(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	before := u.unit("suiteward.container")
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatal(err)
	}
	dirs, err := filepath.Glob(filepath.Join(u.backups, "*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("backup directories: %v, %v", dirs, err)
	}
	dir := dirs[0]
	for file, want := range map[string]string{"suiteward.sql": dumpText, "suiteward-artifacts.tar": "TARBYTES", "status.json": statusBody, "units/suiteward.container": before} {
		if got := readFile(t, filepath.Join(dir, file)); got != want {
			t.Errorf("%s holds %q", file, got)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "suiteward.sql")); err != nil || (info.Mode().Perm() != 0o600 && !isWindows()) {
		t.Errorf("the dump has mode %v, %v", info.Mode().Perm(), err)
	}
}

func isWindows() bool { return runtime.GOOS == "windows" }

func TestUpgradeIsolatedRefreshesTheDatabaseUnitOnANewPin(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	postgres, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(unitDir(u.home), "suiteward-db.container")
	if err := os.WriteFile(path, []byte(strings.Replace(readFile(t, path), "Image="+postgres, "Image="+oldPin, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	u.f.noImage = true
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatalf("an older database pin is what an upgrade refreshes, not a difference: %v", err)
	}
	if text := u.unit("suiteward-db.container"); !strings.Contains(text, "Image="+postgres) || strings.Contains(text, oldPin) {
		t.Errorf("the database unit keeps the old pin:\n%s", text)
	}
	reload := u.f.index("daemon-reload", 0)
	restartDB := u.f.index("systemctl --user restart suiteward-db.service", reload)
	health := u.f.index("inspect --type container --format {{json .State}} suiteward-db", restartDB)
	migrate := u.f.index("systemctl --user restart suiteward-migrate.service", health)
	if reload < 0 || restartDB < 0 || health < 0 || migrate < 0 {
		t.Fatalf("reload, database restart, health wait and migrate are out of order:\n%s", strings.Join(u.f.lines(), "\n"))
	}
	if u.f.index("podman pull "+postgres, 0) < 0 {
		t.Errorf("the new database image was not pulled")
	}
	if !strings.Contains(u.out.String(), oldPin) || !strings.Contains(u.out.String(), postgres) {
		t.Errorf("the plan does not show the pin change:\n%s", u.out.String())
	}
}

func TestUpgradeIsolatedSkipsImagesTheEngineHas(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatal(err)
	}
	if u.f.has("podman pull") {
		t.Errorf("an image the engine has was pulled: %v", u.f.lines())
	}
}

func TestUpgradeIsolatedStopsWhenTheBackupFails(t *testing.T) {
	for name, fail := range map[string]string{"the dump": "pg_dump", "the volume": "volume export"} {
		t.Run(name, func(t *testing.T) {
			u := newIsoUpgrade(t, nil)
			before := u.units()
			u.f.failOn[fail] = errors.New(fail + " failed")
			err := u.upgrade("--image", testDigest, "--yes")
			if err == nil || !strings.Contains(err.Error(), "backup") {
				t.Fatalf("got %v", err)
			}
			u.untouched(before)
			if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
				t.Errorf("a partial backup was left: %v", dirs)
			}
		})
	}
}

func TestUpgradeIsolatedAsksFirst(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	before := u.units()
	if err := u.upgrade("--image", testDigest); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	if u.f.has("pg_dump") {
		t.Error("the backup ran before the confirmation")
	}
	u.untouched(before)
}

func TestUpgradeIsolatedStopsOnAnEditedUnitBeforeTheBackup(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	path := filepath.Join(unitDir(u.home), "suiteward-db.container")
	if err := os.WriteFile(path, []byte(readFile(t, path)+"PodmanArgs=--privileged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := u.units()
	err := u.upgrade("--image", testDigest, "--yes")
	var diff *DiffError
	if !errors.As(err, &diff) {
		t.Fatalf("got %v, want a *DiffError", err)
	}
	if u.f.has("pg_dump") {
		t.Error("a backup was taken before the adoption diff stopped the run")
	}
	u.untouched(before)
}

func TestUpgradeIsolatedRefusesWhenNothingIsInstalled(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	if err := os.RemoveAll(unitDir(u.home)); err != nil {
		t.Fatal(err)
	}
	err := u.upgrade("--image", testDigest, "--yes")
	if err == nil || !strings.Contains(err.Error(), "suiteward install") {
		t.Fatalf("got %v, want a pointer to install", err)
	}
	if u.f.has("pg_dump") || u.f.has("stop") {
		t.Errorf("something ran: %v", u.f.lines())
	}
}

func TestUpgradeIsolatedToTheInstalledReleaseDoesNothing(t *testing.T) {
	u := newIsoUpgrade(t, func(p *UnitParams) { p.Digest = testDigest })
	before := u.units()
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "already on "+testDigest) || !strings.Contains(u.out.String(), "suiteward doctor") {
		t.Errorf("the output must say it is up to date and name doctor:\n%s", u.out.String())
	}
	if u.f.has("pg_dump") {
		t.Error("an up-to-date host was backed up")
	}
	u.untouched(before)
}

func TestUpgradeIsolatedDefaultsToTheReleaseTheBinaryCarries(t *testing.T) {
	was := ImageDigest
	t.Cleanup(func() { ImageDigest = was })
	ImageDigest = testDigest
	u := newIsoUpgrade(t, nil)
	if err := u.upgrade("--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.unit("suiteward.container"), "@"+testDigest) {
		t.Errorf("the embedded release was not installed")
	}
	ImageDigest = ""
	u = newIsoUpgrade(t, nil)
	if err := u.upgrade("--yes"); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Fatalf("a development build must ask for --image: %v", err)
	}
}

func TestUpgradeIsolatedMovesTheEnvFileOffEtc(t *testing.T) {
	const etc = "/etc/suiteward/github.env"
	u := newIsoUpgrade(t, func(p *UnitParams) { p.EnvFile = etc })
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatal(err)
	}
	if text := u.unit("suiteward.container"); !strings.Contains(text, "EnvironmentFile="+DefaultEnvFile) || strings.Contains(text, etc) {
		t.Errorf("the service still reads /etc:\n%s", text)
	}
	if !strings.Contains(u.out.String(), "sudo rm "+etc) {
		t.Errorf("the administrator is not told about the old file:\n%s", u.out.String())
	}

	u = newIsoUpgrade(t, func(p *UnitParams) { p.EnvFile = etc })
	if err := os.Remove(envFile(u.home)); err != nil {
		t.Fatal(err)
	}
	if err := u.upgrade("--image", testDigest, "--yes"); err == nil || !strings.Contains(err.Error(), "install host") {
		t.Fatalf("an env file that was not copied must stop the run: %v", err)
	}
	if u.f.has("pg_dump") {
		t.Error("the backup ran before the env file check")
	}
}

func TestUpgradeIsolatedNamesTheBackupWhenALaterStepFails(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.dryRunSkips = "suiteward-migrate.service"
	err := u.upgrade("--image", testDigest, "--yes")
	if err == nil || !strings.Contains(err.Error(), u.backups) {
		t.Fatalf("a failure after the backup must name it: %v", err)
	}
}

func TestUpgradeIsolatedRefusesAnotherAccount(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.user = "someone"
	if err := u.upgrade("--image", testDigest, "--yes"); err == nil || u.f.has("pg_dump") {
		t.Fatalf("got %v", err)
	}
}

// ---- uninstall ----

var isolatedUnitFiles = []string{
	"suiteward-data.volume", "suiteward-db-data.volume", "suiteward-db.container", "suiteward-migrate.container", "suiteward-network.network", "suiteward.container",
}

func TestUninstallIsolatedRemovesTheUnitsAndKeepsTheState(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	if err := u.uninstall("--yes"); err != nil {
		t.Fatal(err)
	}
	stop := u.f.index("systemctl --user stop suiteward.service", 0)
	reload := u.f.index("systemctl --user daemon-reload", stop)
	if stop < 0 || reload < 0 {
		t.Fatalf("the units were not stopped and reloaded:\n%s", strings.Join(u.f.lines(), "\n"))
	}
	for _, unit := range []string{"suiteward.service", "suiteward-migrate.service", "suiteward-db.service"} {
		if !u.f.has("systemctl --user stop " + unit) {
			t.Errorf("%s was not stopped", unit)
		}
	}
	for _, name := range isolatedUnitFiles {
		if exists(filepath.Join(unitDir(u.home), name)) {
			t.Errorf("%s was not deleted", name)
		}
	}
	if !exists(settingsFile(u.home)) || !exists(envFile(u.home)) {
		t.Error("the settings or the GitHub env file were removed without --purge")
	}
	for _, change := range []string{"secret rm", "volume rm", "network rm"} {
		if u.f.has(change) {
			t.Errorf("uninstall without --purge ran %s", change)
		}
	}
}

func TestUninstallIsolatedPurgeRemovesTheStateToo(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.objects = map[string]bool{VolumeData: true, VolumeDBData: true, NetworkName: true}
	if err := os.MkdirAll(u.backups, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(u.backups, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := u.uninstall("--purge", "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{VolumeData, VolumeDBData} {
		if !u.f.has("volume rm " + name) {
			t.Errorf("the volume %s was not removed", name)
		}
	}
	if !u.f.has("network rm " + NetworkName) {
		t.Error("the network was not removed")
	}
	for _, s := range secretCatalog {
		if !u.f.has("secret rm --ignore " + s.name) {
			t.Errorf("the secret %s was not removed", s.name)
		}
	}
	// The volumes go after the units are stopped: a running database holds its volume.
	if u.f.index("volume rm", 0) < u.f.index("systemctl --user stop suiteward-db.service", 0) {
		t.Error("a volume was removed before the database stopped")
	}
	if exists(settingsFile(u.home)) || exists(envFile(u.home)) {
		t.Error("the settings or the GitHub env file are still there")
	}
	if !exists(filepath.Join(u.backups, "keep")) {
		t.Error("a backup was deleted")
	}
}

func TestUninstallIsolatedStopsOnAnEditedUnitAndPrintsTheDiff(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	path := filepath.Join(unitDir(u.home), "suiteward.container")
	if err := os.WriteFile(path, []byte(readFile(t, path)+"Environment=EXTRA=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := u.units()
	err := u.uninstall("--purge", "--yes")
	var diff *DiffError
	if !errors.As(err, &diff) || !diffMentions(diff, "EXTRA") {
		t.Fatalf("got %v, want a *DiffError naming the edit", err)
	}
	u.untouched(before)
}

func TestUninstallIsolatedAsksFirst(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	before := u.units()
	if err := u.uninstall("--purge"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	u.untouched(before)
}

func TestUninstallIsolatedNeverTouchesEtc(t *testing.T) {
	const etc = "/etc/suiteward/github.env"
	u := newIsoUpgrade(t, func(p *UnitParams) { p.EnvFile = etc })
	if err := u.uninstall("--purge", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "sudo rm "+etc) {
		t.Errorf("the administrator is not told about the file:\n%s", u.out.String())
	}
	for _, line := range u.f.lines() {
		if strings.Contains(line, "/etc/suiteward") && !strings.Contains(line, generatorPath) {
			t.Errorf("a command touched /etc: %s", line)
		}
	}
}

func TestUninstallIsolatedPurgesWhatIsLeftAfterAnEarlierUninstall(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	if err := u.uninstall("--yes"); err != nil {
		t.Fatal(err)
	}
	calls := len(u.f.calls)
	u.f.objects = map[string]bool{VolumeData: true}
	if err := u.uninstall("--purge", "--yes"); err != nil {
		t.Fatalf("the state of an uninstalled stack must still be purgeable: %v", err)
	}
	if !slices.ContainsFunc(u.f.lines()[calls:], func(l string) bool { return strings.Contains(l, "volume rm "+VolumeData) }) {
		t.Errorf("the volume was not removed")
	}
	if err := u.uninstall("--yes"); err == nil || !strings.Contains(err.Error(), "nothing is installed") {
		t.Fatalf("uninstall of nothing without --purge must say so: %v", err)
	}
}

func TestUpgradeIsolatedBeforeMigrateSaysTheDatabaseIsUnchangedAndNamesTheOldUnits(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.dryRunSkips = "suiteward-migrate.service" // a failure after the service was stopped and the units were written
	err := u.upgrade("--image", testDigest, "--yes")
	if err == nil {
		t.Fatal("the dry run must fail")
	}
	dirs, _ := filepath.Glob(filepath.Join(u.backups, "*"))
	if len(dirs) != 1 {
		t.Fatalf("backups: %v", dirs)
	}
	for _, want := range []string{
		"schema and the data are unchanged", dirs[0], filepath.Join(dirs[0], backupUnits), "suiteward.service is stopped",
		// To go back: the old units, then the reload and the start. To go on: the units name the new release, and the migration unit
		// stays active from the old one, so it is restarted, not just the service started.
		"copy them to " + unitDir(u.home) + ", run `systemctl --user daemon-reload` and `systemctl --user start suiteward.service`",
		"run `systemctl --user daemon-reload`, then `systemctl --user restart suiteward-db.service` (only if the database image changed), then `systemctl --user restart suiteward-migrate.service` and `systemctl --user start suiteward.service`",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the advice lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "restore") {
		t.Errorf("a restore loses everything after the backup and the database was not touched:\n%v", err)
	}
}

func TestUpgradeIsolatedFailingToStopTheServiceDoesNotClaimItIsStopped(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.failOn["systemctl --user stop suiteward.service"] = errors.New("stop failed")
	err := u.upgrade("--image", testDigest, "--yes")
	if err == nil || !strings.Contains(err.Error(), "schema and the data are unchanged") || !strings.Contains(err.Error(), "suiteward install --upgrade") || strings.Contains(err.Error(), "is stopped") || strings.Contains(err.Error(), "restore") {
		t.Fatalf("got %v", err)
	}
}

func TestUpgradeIsolatedFromMigrateOnKeepsTheRestoreAdvice(t *testing.T) {
	for name, fail := range map[string]string{
		"migrate": "systemctl --user restart suiteward-migrate.service",
		"service": "systemctl --user start suiteward.service",
	} {
		u := newIsoUpgrade(t, nil)
		u.f.failOn[fail] = errors.New(name + " failed")
		err := u.upgrade("--image", testDigest, "--yes")
		if err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "schema and the data are unchanged") {
			t.Fatalf("%s: once migrate has run the way back is the backup: %v", name, err)
		}
	}
}

func TestUpgradeIsolatedKeepsTheGitHubKeyLinesOrTheirAbsence(t *testing.T) {
	for name, withLines := range map[string]bool{"units of an install with GitHub": true, "units of an install without GitHub": false} {
		// The settings name no App (a host installed by hand, or without GitHub): what the units have is what stays.
		u := newIsoUpgrade(t, nil)
		if !withLines {
			path := filepath.Join(unitDir(u.home), "suiteward.container")
			if err := os.WriteFile(path, withoutLines(t, readFileOrFail(t, path), githubKeyUnitLines...), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		serve := u.unit("suiteward.container")
		for _, line := range githubKeyUnitLines {
			if strings.Contains(serve, line) != withLines {
				t.Errorf("%s: the line %q must be there: %v\n%s", name, line, withLines, serve)
			}
		}
		if !strings.Contains(serve, "@"+testDigest) {
			t.Errorf("%s: the units were not upgraded:\n%s", name, serve)
		}
	}
}

func TestUpgradeIsolatedSaysItKeepsTheInstalledPortsAndTimeout(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	before := u.unit("suiteward.container")
	if err := u.upgrade("--image", testDigest, "--yes", "--http-port", strconv.Itoa(u.port+100), "--job-timeout", "9m"); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	for _, want := range []string{"Keeping the installed HTTP port " + strconv.Itoa(u.port), "Keeping the installed job timeout \"3m\""} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MCP port") {
		t.Errorf("the MCP port was not asked for:\n%s", out)
	}
	for _, line := range []string{"PublishPort=127.0.0.1:" + strconv.Itoa(u.port) + ":8080", "Environment=SUITEWARD_JOB_TIMEOUT=3m"} {
		if !strings.Contains(u.unit("suiteward.container"), line) || !strings.Contains(before, line) {
			t.Errorf("an upgrade keeps %q", line)
		}
	}

	// An invalid value is refused before any change, not ignored.
	u = newIsoUpgrade(t, nil)
	before = u.unit("suiteward.container")
	if err := u.upgrade("--image", testDigest, "--yes", "--http-port", "70000"); err == nil {
		t.Fatal("an invalid port must be refused")
	}
	u.untouched(map[string]string{"suiteward.container": before})
}
