package installer

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

const (
	stateRunning   = `{"Status":"running"}`
	stateHealthy   = `{"Status":"running","Health":{"Status":"healthy"}}`
	stateUnhealthy = `{"Status":"running","Health":{"Status":"unhealthy"}}`
	stateExited    = `{"Status":"exited"}`
)

// dockerRerun is the harness of a second `suiteward install` over a Docker stack that a first one made: the settings file
// and the objects are there, and the containers answer with the given states. A Docker stack is known only by its settings
// file, so the engine is never asked to describe a container beyond its state.
func dockerRerun(t *testing.T, dbState, serveState string) *harness {
	t.Helper()
	first := dockerHarness(t, "windows")
	if err := first.docker(); err != nil {
		t.Fatal(err)
	}
	again := dockerHarness(t, "windows")
	again.dir = first.dir
	again.have(KindContainer, ContainerDB, ContainerServe)
	again.have(KindNetwork, NetworkName)
	again.have(KindVolume, VolumeData, VolumeDBData, "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward")
	for _, file := range []string{"postgres-password", "database-owner-url", "database-url", "mcp-token"} {
		again.haveDockerSecret(file)
	}
	again.first("cat /s/database-url", DatabaseURL(appRole, "x"))
	again.first("--format {{json .State}} suiteward-db", dbState)
	again.f.on("--format {{json .State}} suiteward", serveState)
	again.roleExists = true
	if err := os.WriteFile(again.tokenPath(), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	return again
}

// dockerInstall runs the Docker install over a harness; a database that is run again answers healthy from then on, as a
// new container does.
func dockerInstall(t *testing.T, h *harness, args ...string) error {
	t.Helper()
	env := h.newEnv()
	inner := env.Deps.Exec
	env.Deps.Exec = func(ctx context.Context, name string, a []string, stdin io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(a, " "), "--name suiteward-db ") {
			h.first("--format {{json .State}} suiteward-db", stateHealthy)
		}
		return inner(ctx, name, a, stdin)
	}
	all := append([]string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "docker", "--accept-docker", "--image", testImage}, args...)
	return Install(context.Background(), all, env)
}

// ran says whether the engine was asked to run the container; stopped, whether it was asked to stop it.
func ran(h *harness, container string) bool { return len(h.f.find("--name "+container+" ")) > 0 }

func ranServe(h *harness) bool { return len(h.f.find("--name suiteward --network")) > 0 }

func stopped(h *harness, container string) bool {
	for _, c := range h.f.calls {
		if normalized(c) == "stop --time 45 "+container {
			return true
		}
	}
	return false
}

// A re-run over a stack that is complete, running and set up as the settings say recreates neither the database nor the
// service. Migrate is a one-shot that runs every time.
func TestInstallDockerRerunLeavesAHealthyUnchangedStackRunning(t *testing.T) {
	h := dockerRerun(t, stateHealthy, stateRunning)
	if err := dockerInstall(t, h); err != nil {
		t.Fatal(err)
	}
	if ran(h, ContainerDB) || ranServe(h) {
		t.Errorf("a container that runs as installed was run again:\n%s", strings.Join(h.f.lines(), "\n"))
	}
	if stopped(h, ContainerDB) || stopped(h, ContainerServe) || len(h.f.find("rm --force")) != 0 {
		t.Errorf("a container that runs as installed was removed:\n%s", strings.Join(h.f.lines(), "\n"))
	}
	if !ran(h, ContainerMigrate) {
		t.Error("a re-run does not migrate again")
	}
	if strings.Contains(h.out.String(), "Started") {
		t.Errorf("the output says that something was started:\n%s", h.out.String())
	}
}

// What changed or does not run is created; what runs as it should is left alone.
func TestInstallDockerRerunRecreatesWhatChangedOrStopped(t *testing.T) {
	cases := []struct {
		name      string
		db, serve string
		args      []string
		recreates string // what the service run must carry
		keepsDB   bool
	}{
		{"another HTTP port", stateHealthy, stateRunning, []string{"--http-port", "18081"}, "--publish 127.0.0.1:18081:8080", true},
		{"another job time limit", stateHealthy, stateRunning, []string{"--job-timeout", "7m"}, "SUITEWARD_JOB_TIMEOUT=7m", true},
		{"a service that is not running", stateHealthy, stateExited, nil, " " + testImage, true},
		{"a database that is not healthy", stateUnhealthy, stateRunning, nil, " " + testImage, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := dockerRerun(t, tc.db, tc.serve)
			if err := dockerInstall(t, h, tc.args...); err != nil {
				t.Fatal(err)
			}
			run := h.f.find("--name suiteward --network")
			if len(run) != 1 || !strings.Contains(run[0].line(), tc.recreates) || !stopped(h, ContainerServe) {
				t.Errorf("the service was not stopped and run again (want %q): %v", tc.recreates, run)
			}
			if recreated := ran(h, ContainerDB) && stopped(h, ContainerDB); recreated == tc.keepsDB {
				t.Errorf("the database was recreated: %v, want %v", recreated, !tc.keepsDB)
			}
			if !ran(h, ContainerMigrate) {
				t.Error("a re-run does not migrate again")
			}
		})
	}
}

// A secret that this run makes or rewrites is read by the service only when it starts, so the service is run again; the
// database, which was not touched, is not.
func TestInstallDockerRerunRecreatesTheServiceWhenItsSecretsChange(t *testing.T) {
	t.Run("the MCP token is made again", func(t *testing.T) {
		h := dockerRerun(t, stateHealthy, stateRunning)
		h.first("test -f /s/mcp-token", "no") // the token secret is gone, and so is its copy
		if err := os.Remove(h.tokenPath()); err != nil {
			t.Fatal(err)
		}
		if err := dockerInstall(t, h); err != nil {
			t.Fatal(err)
		}
		if !ranServe(h) || ran(h, ContainerDB) {
			t.Errorf("the service must be run again and the database must not:\n%s", strings.Join(h.f.lines(), "\n"))
		}
	})
	t.Run("the application URL is rewritten", func(t *testing.T) {
		h := dockerRerun(t, stateHealthy, stateRunning)
		h.volumeFiles = map[string]string{
			"suiteward-secrets-suiteward-migrate|database-url": DatabaseURL(appRole, "old-migrate"),
			"suiteward-secrets-suiteward|database-url":         DatabaseURL(appRole, "old-serve"),
		}
		if err := dockerInstall(t, h); err != nil {
			t.Fatal(err)
		}
		if !ranServe(h) || ran(h, ContainerDB) {
			t.Errorf("the service must be run again and the database must not:\n%s", strings.Join(h.f.lines(), "\n"))
		}
	})
}
