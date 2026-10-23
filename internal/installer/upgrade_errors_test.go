package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- the co-located upgrade ----

func TestUpgradeColocatedStopsAtTheStepThatFails(t *testing.T) {
	cases := []struct {
		name        string
		fail        string // the engine call that fails
		args        []string
		want        []string
		afterBackup bool // the backup exists when the step fails
	}{
		{"the engine is not identified", "info --format", nil, []string{errInjected.Error()}, false},
		{"an image of a shape that is not pinned", "", []string{"--image", "ghcr.io/ignisdevne/suiteward:latest"}, []string{"never pins a tag"}, false},
		{"the secrets cannot be read", "secret ls --filter name=" + SecretDBAuth, nil, []string{errInjected.Error()}, false},
		{"the new image cannot be pulled", "pull " + testImage, nil, []string{errInjected.Error(), "nothing was changed", "the backup is in"}, true},
		{"the database cannot be started", "run --detach --replace --name suiteward-db ", nil, []string{errInjected.Error(), "Migrate did not run", "The backup is in"}, true},
		{"migrate fails", "run --rm --replace --name suiteward-migrate", nil, []string{errInjected.Error(), "restore it with the previous image"}, true},
		{"the service cannot be started", "run --detach --replace --name suiteward --network", nil, []string{errInjected.Error(), "restore it with the previous image"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpgradeHarness(t, nil)
			if tc.fail != "" {
				u.failFirst(tc.fail)
			}
			err := u.upgrade(append([]string{"--image", testImage, "--yes"}, tc.args...)...)
			if err == nil {
				t.Fatal("the upgrade succeeded although a step failed")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error lacks %q: %v", want, err)
				}
			}
			dirs, _ := filepath.Glob(filepath.Join(u.backups, "*"))
			if tc.afterBackup != (len(dirs) == 1) {
				t.Errorf("backups = %v, want one: %v", dirs, tc.afterBackup)
			}
			if !tc.afterBackup {
				u.noChanges()
			}
			u.f.assertNoSecret(t, "HUNTER2-TOTP")
		})
	}
}

// A status that the backup cannot read (the service answered the probe before it) leaves no backup, and nothing is changed.
func TestUpgradeColocatedWithoutAStatusMakesNoBackup(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.status = failAfterFirst(u.status)
	err := u.upgrade("--image", testImage, "--yes")
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "read GET /status") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("upgrade = %v", err)
	}
	u.noChanges()
	if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
		t.Errorf("a backup was left: %v", dirs)
	}
}

func TestUpgradeColocatedBackupParentThatIsAFile(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := os.WriteFile(u.backups, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("upgrade = %v", err)
	}
	u.noChanges()
}

func TestInstalledDBImageNeedsAnInspectionItCanRead(t *testing.T) {
	ctx := context.Background()
	rt, f := linuxPodman(t)
	f.fail("inspect --type container suiteward-db", errInjected)
	if _, err := installedDBImage(ctx, rt); !errors.Is(err, errInjected) {
		t.Errorf("a failing inspect = %v", err)
	}
	rt, f = linuxPodman(t)
	f.on("inspect --type container suiteward-db", "not json")
	if _, err := installedDBImage(ctx, rt); err == nil || !strings.Contains(err.Error(), "suiteward-db") {
		t.Errorf("an inspection that is not JSON = %v", err)
	}
}

// Inside a Podman machine the status is read through the machine's ssh, as the user the connection names.
func TestReadStatusInsideThePodmanMachine(t *testing.T) {
	f := newFake("windows").on("info --format", "true\n").on("system connection list", machineConnections).on("machine ssh", statusBody)
	rt, err := NewPodman(context.Background(), f.deps(), "podman-machine-default")
	if err != nil {
		t.Fatal(err)
	}
	c := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: Env{Deps: f.deps()}}}
	c.s.Connection, c.s.HTTPPort = "podman-machine-default", 18081
	got, err := c.readStatus(context.Background())
	if err != nil || string(got) != statusBody {
		t.Fatalf("readStatus = %q, %v", got, err)
	}
	ssh := f.find("machine ssh")
	if len(ssh) != 1 || !strings.Contains(ssh[0].line(), "--username user podman-machine-default curl -fsS -m 5 http://127.0.0.1:18081/status") {
		t.Errorf("the status was not read in the machine: %v", f.lines())
	}

	f.injectFirst("system connection list")
	c.machineUser = ""
	if _, err := c.readStatus(context.Background()); !errors.Is(err, errInjected) {
		t.Errorf("readStatus without a machine user = %v", err)
	}
}

// ---- the isolated upgrade ----

func TestUpgradeIsolatedStopsAtTheStepThatFails(t *testing.T) {
	// never lists the commands that must not have run: every step after the one that failed.
	cases := []struct {
		name  string
		setup func(u *isoUpgrade)
		args  []string
		want  []string
		never []string
	}{
		{"an image of another repository", nil, []string{"--image", "docker.io/library/other@" + testDigest}, []string{"the isolated tier runs"},
			[]string{"pg_dump", "systemctl --user stop", "daemon-reload"}},
		{"an image that is a tag", nil, []string{"--image", "ghcr.io/ignisdevne/suiteward:v1"}, []string{"never pins a tag"},
			[]string{"pg_dump", "systemctl --user stop", "daemon-reload"}},
		{"the new image cannot be pulled", func(u *isoUpgrade) {
			u.f.noImage = true
			u.f.failOn["podman pull "+releaseImageRepo] = errInjected
		}, nil, []string{errInjected.Error(), "nothing was changed", "the backup is in"},
			[]string{"systemctl --user stop", "daemon-reload", "restart suiteward-migrate.service", "start suiteward.service"}},
		{"the service cannot be stopped", func(u *isoUpgrade) { u.f.failOn["systemctl --user stop suiteward.service"] = errInjected }, nil,
			[]string{"stop the service", "Migrate did not run", "Fix the cause"},
			[]string{generatorPath, "daemon-reload", "restart suiteward-migrate.service", "start suiteward.service"}},
		{"the generator rejects the units", func(u *isoUpgrade) { u.f.failOn[generatorPath] = errInjected }, nil,
			[]string{"the Podman generator rejects the units", "suiteward.service is stopped"},
			[]string{"daemon-reload", "restart suiteward-migrate.service", "start suiteward.service"}},
		{"the daemon reload fails", func(u *isoUpgrade) { u.f.failOn["daemon-reload"] = errInjected }, nil,
			[]string{"daemon-reload", "suiteward.service is stopped"},
			[]string{"restart suiteward-db.service", "restart suiteward-migrate.service", "start suiteward.service"}},
		{"migrate fails", func(u *isoUpgrade) { u.f.failOn["restart suiteward-migrate.service"] = errInjected }, nil,
			[]string{"the migration failed, so the service was not started", "restore it with the previous image"},
			[]string{"start suiteward.service", "--property=ActiveState"}},
		{"the service does not start", func(u *isoUpgrade) { u.f.failOn["start suiteward.service"] = errInjected }, nil,
			[]string{"start the service", "restore it with the previous image"},
			[]string{"--property=ActiveState"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newIsoUpgrade(t, nil)
			if tc.setup != nil {
				tc.setup(u)
			}
			unitsBefore := u.units()
			err := u.upgrade(append([]string{"--image", testDigest, "--yes"}, tc.args...)...)
			u.assertStoppedWith(t, err, tc.want, tc.never)
			if tc.name == "the service cannot be stopped" {
				u.assertUnitsAre(t, unitsBefore)
			}
		})
	}
}

// assertStoppedWith checks a failed isolated upgrade: the error says what it should, none of the commands after the failing step
// ran, and the settings still name the old release, which they do only once the service is up on the new one.
func (u *isoUpgrade) assertStoppedWith(t *testing.T, err error, want, never []string) {
	t.Helper()
	if err == nil {
		t.Fatal("the upgrade succeeded although a step failed")
	}
	for _, text := range want {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("the error lacks %q: %v", text, err)
		}
	}
	for _, command := range never {
		if u.f.has(command) {
			t.Errorf("%q ran after the failure: %v", command, u.f.lines())
		}
	}
	if got, loadErr := LoadSettings(settingsFile(u.home)); loadErr != nil || got.Image != oldImage {
		t.Errorf("the settings after the failure = %q, %v; want the old image", got.Image, loadErr)
	}
}

// assertUnitsAre fails when a unit file differs from what it was.
func (u *isoUpgrade) assertUnitsAre(t *testing.T, before map[string]string) {
	t.Helper()
	for name, text := range before {
		if got := u.unit(name); got != text {
			t.Errorf("%s was replaced although the service could not be stopped", name)
		}
	}
}

func TestUpgradeIsolatedRefusesWhatItCannotReadBeforeTheBackup(t *testing.T) {
	t.Run("a unit that cannot be read", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		unit := filepath.Join(unitDir(u.home), "suiteward.container")
		if err := os.Remove(unit); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(unit, 0o700); err != nil {
			t.Fatal(err)
		}
		err := u.upgrade("--image", testDigest, "--yes")
		if err == nil || !strings.Contains(err.Error(), "read the installed unit suiteward.container") {
			t.Fatalf("upgrade = %v", err)
		}
		if u.f.has("pg_dump") {
			t.Error("a backup was taken of units that could not be read")
		}
	})
	t.Run("a status that cannot be read", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		env := u.env()
		env.HTTPGet = failAfterFirst(env.httpGet())
		fastHealthWait(t)
		err := Install(context.Background(), []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}, env)
		if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("upgrade = %v", err)
		}
		if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
			t.Errorf("a backup was left: %v", dirs)
		}
	})
	t.Run("a database pin that changes and cannot be restarted", func(t *testing.T) {
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
		u.f.failOn["restart suiteward-db.service"] = errInjected
		err = u.upgrade("--image", testDigest, "--yes")
		if err == nil || !strings.Contains(err.Error(), "restart the database") || !strings.Contains(err.Error(), "suiteward.service is stopped") {
			t.Fatalf("upgrade = %v", err)
		}
	})
}

// ---- the co-located uninstall ----

func TestUninstallColocatedStopsAtTheStepThatFails(t *testing.T) {
	cases := []struct {
		name  string
		fail  string
		purge bool
		never []string // commands that must not have run after the failure
	}{
		{"a container cannot be removed", "rm --force --ignore --time 45 suiteward-migrate", false, []string{"rm --force --ignore --time 45 suiteward-db"}},
		{"a volume cannot be removed", "volume rm suiteward-data", true, []string{"volume rm suiteward-db-data", "secret rm", "network rm"}},
		{"a secret cannot be removed", "secret rm", true, []string{"network rm"}},
		{"the network cannot be removed", "network rm", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "linux")
			podmanStackExists(h)
			h.writeFiles()
			h.failFirst(tc.fail)
			args := []string{"--runtime", "podman", "--connection", "pm", "--yes"}
			if tc.purge {
				args = append(args, "--purge")
			}
			if err := h.uninstall(args...); !errors.Is(err, errInjected) {
				t.Fatalf("uninstall = %v", err)
			}
			for _, never := range tc.never {
				if len(h.f.find(never)) != 0 {
					t.Errorf("%q ran after the failure: %v", never, h.f.lines())
				}
			}
			if !exists(h.settingsPath()) || !exists(h.tokenPath()) {
				t.Error("the settings or the token copy were removed although the purge failed before them")
			}
		})
	}
}

func TestUninstallPurgeStopsOnAFileItCannotRemove(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	h.writeFiles()
	if err := os.Remove(h.tokenPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.tokenPath(), "inside"), 0o700); err != nil { // a directory that is not empty cannot be removed as a file
		t.Fatal(err)
	}
	err := h.uninstall("--runtime", "podman", "--connection", "pm", "--yes", "--purge")
	if err == nil || !strings.Contains(err.Error(), "remove "+h.tokenPath()) {
		t.Fatalf("uninstall = %v", err)
	}
	if !exists(h.settingsPath()) {
		t.Error("the settings file was removed although the token copy could not be")
	}
}

func TestUninstallColocatedRefusesWhatItCannotReadBeforeRemovingAnything(t *testing.T) {
	t.Run("a purge without a place for the token copy", func(t *testing.T) {
		h := newHarness(t, "windows")
		h.f.on("system connection list", machineConnections)
		podmanStackExists(h)
		err := Uninstall(context.Background(), []string{"--settings", h.settingsPath(), "--runtime", "podman", "--connection", "podman-machine-default", "--purge", "--yes"}, h.newEnv())
		if err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
			t.Fatalf("uninstall = %v", err)
		}
	})
	t.Run("a console that cannot ask", func(t *testing.T) {
		h := newHarness(t, "linux")
		env := h.newEnv()
		env.Term.IsTerminal = true
		if err := Uninstall(context.Background(), []string{"--settings", h.settingsPath()}, env); err == nil || !strings.Contains(err.Error(), "password reader") {
			t.Fatalf("uninstall = %v", err)
		}
	})
	t.Run("no place for the settings", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("AppData", "")
		t.Setenv("HOME", "")
		h := newHarness(t, "linux")
		if err := Uninstall(context.Background(), []string{"--yes"}, h.newEnv()); err == nil || !strings.Contains(err.Error(), "user configuration directory") {
			t.Fatalf("uninstall = %v", err)
		}
	})
	t.Run("settings that are not valid", func(t *testing.T) {
		h := newHarness(t, "linux")
		if err := os.WriteFile(h.settingsPath(), []byte("tier: wrong\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := h.uninstall("--yes"); err == nil || !strings.Contains(err.Error(), "tier") {
			t.Fatalf("uninstall = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("an engine that cannot be reached", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.failFirst("info --format")
		if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); !errors.Is(err, errInjected) {
			t.Fatalf("uninstall = %v", err)
		}
		h.mustNotMutate()
	})
}

// ---- the isolated uninstall ----

func TestUninstallIsolatedStopsAtTheStepThatFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(u *isoUpgrade)
		purge bool
		want  string
	}{
		{"another account", func(u *isoUpgrade) { u.f.user = "someone" }, false, "not as suiteward"},
		{"a unit that cannot be read", func(u *isoUpgrade) {
			unit := filepath.Join(unitDir(u.home), "suiteward.container")
			if err := os.Remove(unit); err != nil {
				u.t.Fatal(err)
			}
			if err := os.Mkdir(unit, 0o700); err != nil {
				u.t.Fatal(err)
			}
		}, false, "read the installed unit"},
		{"the state of a unit", func(u *isoUpgrade) { u.f.failOn["show --property=ActiveState --value suiteward.service"] = errInjected }, false, "ask for the state of suiteward.service"},
		{"the stop of a unit", func(u *isoUpgrade) { u.f.failOn["stop suiteward-migrate.service"] = errInjected }, false, "stop suiteward-migrate.service"},
		{"the daemon reload", func(u *isoUpgrade) { u.f.failOn["daemon-reload"] = errInjected }, false, "daemon-reload"},
		{"a secret of a purge", func(u *isoUpgrade) { u.f.failOn["secret rm"] = errInjected }, true, "remove the secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newIsoUpgrade(t, nil)
			tc.setup(u)
			args := []string{"--yes"}
			if tc.purge {
				args = append(args, "--purge")
			}
			err := u.uninstall(args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("uninstall = %v, want an error containing %q", err, tc.want)
			}
			if tc.purge {
				if _, statErr := os.Stat(settingsFile(u.home)); statErr != nil {
					t.Errorf("the settings file was removed although the purge failed: %v", statErr)
				}
			}
		})
	}
}

// failAfterFirst answers the first status read (the probe of a running service) and fails every later one.
func failAfterFirst(get func(context.Context, string) ([]byte, error)) func(context.Context, string) ([]byte, error) {
	calls := 0
	return func(ctx context.Context, url string) ([]byte, error) {
		if calls++; calls == 1 {
			return get(ctx, url)
		}
		return nil, errInjected
	}
}
