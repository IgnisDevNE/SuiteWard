package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A flag that cannot be read stops the install before anything is changed, and the error names the flag.
func TestInstallRefusesAnUnreadableSettingBeforeChangingAnything(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"http port", []string{"--runtime", "podman", "--image", testImage, "--http-port", "eighty"}, "http-port"},
		{"mcp port", []string{"--runtime", "podman", "--image", testImage, "--mcp-port", "eighty"}, "mcp-port"},
		{"image", []string{"--runtime", "podman", "--image", "ghcr.io/x/y:latest"}, "never pins a tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "linux")
			err := h.install(append([]string{"--connection", "pm"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want an error containing %q", err, tc.want)
			}
			h.mustNotMutate()
		})
	}
}

// An engine that cannot be identified is refused before the install looks at the stack.
func TestInstallRefusesAnEngineItCannotIdentify(t *testing.T) {
	h := newHarness(t, "linux")
	h.failFirst("info --format")
	err := h.podman()
	if !errors.Is(err, errInjected) {
		t.Fatalf("podman: %v", err)
	}
	h.mustNotMutate()

	d := dockerHarness(t, "linux")
	d.failFirst("version --format")
	if err := d.docker(); !errors.Is(err, errInjected) {
		t.Fatalf("docker: %v", err)
	}
	d.mustNotMutate()
}

// An existing stack that cannot be inspected or adopted stops the install: guessing at it could replace what a user made.
func TestInstallStopsWhenTheExistingStackCannotBeRead(t *testing.T) {
	t.Run("inspect fails", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.existingStack(colocatedSettings(), testImage, nil)
		h.failFirst("inspect --type container suiteward-db")
		if err := h.podman(); !errors.Is(err, errInjected) {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("inspect is not JSON", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.existingStack(colocatedSettings(), testImage, nil)
		h.first("inspect --type container suiteward-db", "{not json")
		err := h.podman()
		if err == nil || !strings.Contains(err.Error(), "suiteward-db") {
			t.Fatalf("install = %v, want an error that names the container", err)
		}
		h.mustNotMutate()
	})
	t.Run("a release other than the one that runs", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.existingStack(colocatedSettings(), testImage, nil)
		err := h.podman("--image", "ghcr.io/x/y:latest")
		if !errors.Is(err, ErrNotDigestImage) {
			t.Fatalf("install = %v, want ErrNotDigestImage", err)
		}
		h.mustNotMutate()
	})
}

func TestPodmanConnectionRefusesDepsWithoutAnExec(t *testing.T) {
	if _, err := podmanConnection(context.Background(), Deps{GOOS: "windows", Getenv: func(string) string { return "" }}, ""); err == nil {
		t.Fatal("a connection was chosen without a way to run podman")
	}
	got, err := podmanConnection(context.Background(), Deps{GOOS: "windows", Getenv: func(k string) string {
		if k == "CONTAINER_CONNECTION" {
			return "from-env"
		}
		return ""
	}}, "")
	if err != nil || got != "from-env" {
		t.Fatalf("got %q, %v; want CONTAINER_CONNECTION", got, err)
	}
}

func TestDefaultTokenFile(t *testing.T) {
	deps := func(goos string, env map[string]string) Deps {
		return Deps{GOOS: goos, Getenv: func(k string) string { return env[k] }}
	}
	local := t.TempDir()
	if got, err := defaultTokenFile(deps("windows", map[string]string{"LOCALAPPDATA": local})); err != nil || got != filepath.Join(local, "SuiteWard", "mcp-token") {
		t.Errorf("windows: %q, %v", got, err)
	}
	if _, err := defaultTokenFile(deps("windows", nil)); err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
		t.Errorf("windows without LOCALAPPDATA: %v", err)
	}

	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AppData", config)
	t.Setenv("HOME", config)
	// os.UserConfigDir reads the host's own variables: XDG_CONFIG_HOME on Linux, AppData on Windows, HOME/Library on macOS.
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := defaultTokenFile(deps("linux", nil)); err != nil || got != filepath.Join(userConfig, "suiteward", "mcp-token") || !strings.HasPrefix(got, config) {
		t.Errorf("linux: %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	t.Setenv("HOME", "")
	if _, err := defaultTokenFile(deps("linux", nil)); err == nil || !strings.Contains(err.Error(), "user configuration directory") {
		t.Errorf("no configuration directory: %v", err)
	}
}

// Without --token-file the copy goes to the default place; when that cannot be found the install stops before any change.
func TestInstallStopsWhenTheTokenFileHasNoDefaultPlace(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.on("system connection list", machineConnections)
	err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--runtime", "podman", "--connection", "podman-machine-default", "--image", testImage}, h.newEnv())
	if err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
		t.Fatalf("install = %v, want the missing LOCALAPPDATA", err)
	}
	h.mustNotMutate()
}

// The secrets of an earlier run are read before anything is changed; one that cannot be read stops the install.
func TestInstallStopsWhenAnExistingSecretCannotBeRead(t *testing.T) {
	t.Run("podman: the application URL", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL)
		h.failFirst("secret inspect --showsecret --format {{.SecretData}} " + SecretDatabaseURL)
		// The engine's message may quote a secret, so the error says what failed and not why.
		err := h.podman()
		if err == nil || !strings.Contains(err.Error(), "read the secret "+SecretDatabaseURL) || strings.Contains(err.Error(), errInjected.Error()) {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("podman: the database password of a half-made stack", func(t *testing.T) {
		h := newHarness(t, "linux")
		h.have(kindSecret, SecretDBAuth)
		h.failFirst("secret inspect --showsecret --format {{.SecretData}} " + SecretDBAuth)
		if err := h.podman(); err == nil || !strings.Contains(err.Error(), "read the secret "+SecretDBAuth) {
			t.Fatalf("install = %v", err)
		}
		h.f.assertNoSecret(t, h.secretValues()...)
		if len(h.f.find("--name suiteward --network")) != 0 {
			t.Error("the service was started after the failure")
		}
	})
	t.Run("docker: the copies of the application URL", func(t *testing.T) {
		h := dockerHarness(t, "linux")
		h.have(KindVolume, "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward")
		for _, file := range []string{"postgres-password", "database-owner-url", "database-url"} {
			h.haveDockerSecret(file)
		}
		h.first("cat /s/database-url", DatabaseURL(appRole, "x"))
		h.failFirst("suiteward-secrets-suiteward:/s")
		if err := h.docker(); err == nil || !strings.Contains(err.Error(), SecretDatabaseURL) {
			t.Fatalf("install = %v", err)
		}
		// The probes of the secret volumes are read-only runs; nothing was created or started.
		if len(h.f.find("network create")) != 0 || len(h.f.find("volume create")) != 0 || len(h.f.find("--detach")) != 0 || len(h.f.find("cat > /s/")) != 0 {
			t.Errorf("something was changed before the refusal: %v", h.f.lines())
		}
	})
}

// A token copy whose state cannot be told is refused rather than overwritten, and a copy that cannot be written stops the install
// before the secret exists, so no token is held that no file carries.
func TestInstallTokenFileErrors(t *testing.T) {
	t.Run("the path cannot be examined", func(t *testing.T) {
		h := newHarness(t, "linux")
		err := h.podman("--token-file", h.dir+"/bad\x00name")
		if err == nil || !strings.Contains(err.Error(), "look for") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("the directory cannot be made", func(t *testing.T) {
		h := newHarness(t, "linux")
		file := filepath.Join(h.dir, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := h.podman("--token-file", filepath.Join(file, "sub", "mcp-token"))
		if err == nil {
			t.Fatal("install succeeded with a token file under a regular file")
		}
		if got := h.secretWrites(SecretMCPAuth); len(got) != 0 {
			t.Errorf("the token secret was written although its copy was not: %v", got)
		}
	})
	t.Run("the path is a directory", func(t *testing.T) {
		h := newHarness(t, "linux")
		dir := filepath.Join(h.dir, "mcp-token")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// The directory exists, so checkTokenFile refuses it as another instance's file, before anything is changed.
		err := h.podman("--token-file", dir)
		if err == nil || !strings.Contains(err.Error(), "belongs to another instance") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("the copy cannot be restricted", func(t *testing.T) {
		h := newHarness(t, "windows")
		h.f.on("system connection list", machineConnections)
		h.f.fail("/inheritance:r", errors.New("access denied"))
		err := h.podman("--connection", "podman-machine-default")
		if err == nil || !strings.Contains(err.Error(), "restrict") || !strings.Contains(err.Error(), "access denied") {
			t.Fatalf("install = %v", err)
		}
		if got := h.secretWrites(SecretMCPAuth); len(got) != 0 {
			t.Errorf("the token secret was written although its copy is not restricted: %v", got)
		}
	})
}

func TestWriteTokenFileFailsWhenTheFileCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be: the parent can be made, the file cannot be opened for writing.
	target := filepath.Join(dir, "mcp-token")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	err := writeTokenFile(context.Background(), Deps{GOOS: "linux"}, target, "t")
	if err == nil || !strings.Contains(err.Error(), "create "+target) {
		t.Fatalf("writeTokenFile = %v", err)
	}
}

// A cancelled install does not wait ninety seconds on a database nobody will start.
func TestInstallStopsWhenItsContextIsCancelled(t *testing.T) {
	h := newHarness(t, "linux")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Install(ctx, []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}, h.newEnv())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("install = %v, want context.Canceled", err)
	}
	if len(h.f.find("--name suiteward --network")) != 0 {
		t.Error("the service was started by a cancelled install")
	}
}

// A machine connection that cannot be read is a note, not a stop: the ports are checked when the service starts.
func TestInstallNotesAMachineItCannotLookInto(t *testing.T) {
	t.Run("the connection list fails", func(t *testing.T) {
		h := newHarness(t, "windows")
		h.failFirst("system connection list")
		if err := h.podman("--connection", "podman-machine-default"); err != nil {
			t.Fatalf("install = %v", err)
		}
		out := h.out.String()
		if !strings.Contains(out, "Could not check the ports inside the Podman machine") || !strings.Contains(out, "list the Podman connections") {
			t.Errorf("the failure is not reported:\n%s", out)
		}
		if !strings.Contains(out, "Could not turn on podman-restart.service") {
			t.Errorf("the restart failure is not reported:\n%s", out)
		}
	})
	for _, tc := range []struct{ name, list, want string }{
		{"the output is not JSON", "not json", "read the Podman connections"},
		{"the connection has no ssh user", `[{"Name":"podman-machine-default","URI":"ssh://127.0.0.1:1/run/podman.sock"}]`, "has no ssh user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "windows")
			h.first("system connection list", tc.list)
			if err := h.podman("--connection", "podman-machine-default"); err != nil {
				t.Fatalf("install = %v", err)
			}
			if !strings.Contains(h.out.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, h.out.String())
			}
		})
	}
}

// A key that cannot be stored stops the install before the service is replaced, and the key is not echoed.
func TestInstallStopsWhenTheAppKeySecretCannotBeWritten(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.failFirst("secret create suiteward-github-app-key")
	if err := h.podman(githubFlags...); !errors.Is(err, errInjected) {
		t.Fatalf("install = %v", err)
	}
	h.f.assertNoSecret(t, testPEM, "MIIEvQIBADANBgkq")
	for _, c := range h.f.calls {
		if strings.HasPrefix(normalized(c), "rm --force --ignore") {
			t.Errorf("the service was removed although its key was not stored: %v", c.args)
		}
	}
}

// An owner who cannot be looked up is not enrolled again: setup would replace the TOTP secret of one that exists. The install
// still succeeds and says what to check.
func TestInstallDoesNotRunSetupWhenStatusCannotBeRead(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.status = func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	if err := h.podman(githubFlags...); err != nil {
		t.Fatalf("install = %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "The owner setup did not run") || !strings.Contains(out, "connection refused") {
		t.Errorf("the output does not say why setup was not run: %s", out)
	}
	if len(h.f.tty) != 0 {
		t.Errorf("setup ran although the owner could not be looked up: %v", h.f.tty)
	}
}

func TestEngineCommandRefusesARuntimeItDoesNotDrive(t *testing.T) {
	if _, err := engineCommand(nil, "linux"); err == nil || !strings.Contains(err.Error(), "is not a container engine") {
		t.Errorf("engineCommand(nil) = %v", err)
	}
	spaced := &Podman{cli: cli{bin: `C:\Program Files\podman.exe`, base: []string{"--connection", "pm"}}}
	if got, err := engineCommand(spaced, "windows"); err != nil || got != `& "C:\Program Files\podman.exe" --connection pm` {
		t.Errorf("windows: %q, %v", got, err)
	}
	if got, err := engineCommand(spaced, "linux"); err != nil || got != `"C:\Program Files\podman.exe" --connection pm` {
		t.Errorf("linux: %q, %v", got, err)
	}
	docker := &Docker{cli: cli{bin: "docker", base: []string{"--context", "dd"}}}
	if got, err := engineCommand(docker, "linux"); err != nil || got != "docker --context dd" {
		t.Errorf("docker: %q, %v", got, err)
	}
}
