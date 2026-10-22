package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const localImage = "localhost/suiteward@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// ---- errors never carry what a command printed ----

func TestInstallMigrateFailureShowsTheTailOfItsOutputOnly(t *testing.T) {
	h := newHarness(t, "linux")
	h.f.rules = append(h.f.rules, rule{
		match: "--name suiteward-migrate ", out: "FIRSTLINE " + strings.Repeat("log ", 1000) + "migration 21 failed", err: errors.New("exit status 1"),
	})
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), "migration 21 failed") {
		t.Fatalf("the migrate output is not in the error: %v", err)
	}
	if strings.Contains(err.Error(), "FIRSTLINE") || len(err.Error()) > 3000 {
		t.Fatalf("the error is not the bounded tail of the output: %d bytes", len(err.Error()))
	}
}

func TestASecretReadNeverShowsTheValueInItsError(t *testing.T) {
	const value = "s3cret-value"
	f := newFake("linux")
	f.rules = append(f.rules, rule{match: "secret inspect", out: value, err: errors.New("inspect failed: " + value)})
	rt := newRootlessPodman(t, f, "")
	out, err := rt.Secrets().Read(context.Background(), SecretMCPAuth)
	if err == nil || strings.Contains(err.Error(), value) || out != nil {
		t.Fatalf("podman: %q, %v", out, err)
	}
	d := newFake("linux")
	d.rules = append(d.rules, rule{match: "cat /s/mcp-token", out: value, err: errors.New("cat failed: " + value)})
	dk := newDocker(t, d, dockerDesktopServer, `[]`, DockerOptions{})
	out, err = dk.Secrets().Read(context.Background(), SecretMCPAuth)
	if err == nil || strings.Contains(err.Error(), value) || out != nil {
		t.Fatalf("docker: %q, %v", out, err)
	}
}

func TestEngineCommandRefusesARuntimeItDoesNotKnow(t *testing.T) {
	if _, err := engineCommand(notAnEngine{}, "linux"); err == nil {
		t.Fatal("an unknown runtime was given a command")
	}
}

type notAnEngine struct{ Runtime }

// ---- a local image ----

func TestInstallSkipsThePullWhenTheImagesAreHere(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("image inspect", "[{}]")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if pulls := h.f.find("pull "); len(pulls) != 0 {
		t.Errorf("an image that is here was pulled: %v", h.f.lines())
	}
}

func TestInstallPullsOnlyTheImageThatIsMissing(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("image inspect "+testImage, "[{}]")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	pulls := h.f.find("pull ")
	pg, _ := PostgresImage()
	if len(pulls) != 1 || !strings.HasSuffix(pulls[0].line(), pg) {
		t.Errorf("pulls: %v", pulls)
	}
}

func TestInstallTakesALocalImagePinnedByDigest(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("image inspect", "[{}]")
	if err := h.install("--runtime", "podman", "--connection", "pm", "--image", localImage); err != nil {
		t.Fatal(err)
	}
	if run := h.f.find("--name suiteward --network"); len(run) != 1 || !strings.Contains(run[0].line(), " "+localImage) {
		t.Errorf("the service did not start from the local image: %v", run)
	}
}

// ---- a second instance ----

func TestInstallRefusesAnotherPodmanConnectionOnTheDefaultSettings(t *testing.T) {
	useDefaultConfigDir(t)
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	first := DefaultSettings()
	first.Connection = "pm1"
	if err := SaveSettings(path, first); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "linux")
	err = h.installAt("--runtime", "podman", "--connection", "pm2", "--image", testImage)
	if err == nil || !strings.Contains(err.Error(), "--settings") || !strings.Contains(err.Error(), "pm1") {
		t.Fatalf("got %v, want a message about --settings that names the other connection", err)
	}
	h.mustNotMutate()
	if got, _ := LoadSettings(path); got.Connection != "pm1" {
		t.Error("the settings of the first instance were changed")
	}
}

// ---- Docker repairs ----

func TestInstallDockerRepairsServeAndMigrateHoldingDifferentAppURLs(t *testing.T) {
	h := dockerHarness(t, "windows")
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData, "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward")
	for _, file := range []string{"postgres-password", "database-owner-url", "database-url", "mcp-token"} {
		h.haveDockerSecret(file)
	}
	h.volumeFiles = map[string]string{
		"suiteward-secrets-suiteward-migrate|database-url": DatabaseURL(appRole, "aaa"),
		"suiteward-secrets-suiteward|database-url":         DatabaseURL(appRole, "bbb"),
	}
	h.roleExists = true
	if err := os.WriteFile(h.tokenPath(), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.docker(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(sqlOf(h), "\n"), "ALTER ROLE suiteward_app PASSWORD '") {
		t.Error("the role was not given a new password")
	}
	writes := h.secretWrites(SecretDatabaseURL)
	if len(writes) != 2 || writes[0] != writes[1] || strings.Contains(writes[0], "aaa") || strings.Contains(writes[0], "bbb") {
		t.Errorf("both volumes should get the same new URL: %q", writes)
	}
}

func TestInstallSavesTheSettingsBeforeTheServiceStarts(t *testing.T) {
	h := dockerHarness(t, "windows")
	if err := h.docker(); err != nil {
		t.Fatal(err)
	}
	if !h.atServe["settings"] {
		t.Error("the settings did not exist when the service started")
	}
}

func TestInstallDoesNotStartTheServiceWhenTheSettingsCannotBeSaved(t *testing.T) {
	was := syncFile
	syncFile = func(*os.File) error { return errors.New("disk full") }
	t.Cleanup(func() { syncFile = was })
	h := dockerHarness(t, "windows")
	err := h.docker()
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
	if len(h.f.find("--name suiteward --network")) != 0 {
		t.Error("the service started although the settings were not saved: a Docker re-run would find a container it cannot recognize")
	}
}

// ---- inside the Podman machine ----

func machineHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "windows")
	h.f.on("system connection list", machineConnections)
	return h
}

func TestInstallChecksThePortsInsideThePodmanMachine(t *testing.T) {
	h := machineHarness(t)
	h.busy[18081], h.busy[18091] = true, true // busy on the Windows host only: the published ports live in the machine
	h.f.on("machine ssh", "State Recv-Q Send-Q Local Address:Port Peer Address:Port\nLISTEN 0 4096 127.0.0.1:2222 0.0.0.0:*\n")
	if err := h.podman("--connection", "podman-machine-default", "--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatalf("a port busy on the host only was refused: %v", err)
	}
	if ss := h.f.find("machine ssh --username user podman-machine-default ss -ltn"); len(ss) == 0 {
		t.Errorf("the ports were not read inside the machine: %v", h.f.lines())
	}

	h = machineHarness(t)
	h.f.on("machine ssh", "LISTEN 0 4096 127.0.0.1:18091 0.0.0.0:*\nLISTEN 0 4096 *:22 *:*\n")
	err := h.podman("--connection", "podman-machine-default", "--http-port", "18081", "--mcp-port", "18091")
	if err == nil || !strings.Contains(err.Error(), "18091") || !strings.Contains(err.Error(), "Podman machine") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallSaysHowToReadStatusInsideThePodmanMachine(t *testing.T) {
	h := machineHarness(t)
	if err := h.podman("--connection", "podman-machine-default"); err != nil {
		t.Fatal(err)
	}
	want := `podman machine ssh --username user podman-machine-default "curl -s http://127.0.0.1:8081/status"`
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("the output lacks %q:\n%s", want, h.out.String())
	}
	h = dockerHarness(t, "windows")
	if err := h.docker("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	if out := h.out.String(); !strings.Contains(out, "curl -s http://127.0.0.1:18081/status") || strings.Contains(out, "machine ssh") {
		t.Errorf("the status hint for Docker: %s", out)
	}
}

func TestInstallReportsItsProgress(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	wait, migrated := strings.Index(out, "Waiting for suiteward-db"), strings.Index(out, "suiteward migrate finished")
	if wait < 0 || migrated < wait {
		t.Errorf("progress lines missing or out of order:\n%s", out)
	}
}

// ---- uninstall ----

func (h *harness) writeFiles() {
	h.t.Helper()
	s := colocatedSettings()
	s.Connection = "pm"
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.tokenPath(), []byte("x"), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// removed returns the names an engine was asked to remove with verb, for example "volume rm".
func (h *harness) removed(verb string) []string {
	var names []string
	for _, c := range h.f.calls {
		if rest, ok := strings.CutPrefix(normalized(c), verb+" "); ok {
			names = append(names, strings.Fields(rest)[len(strings.Fields(rest))-1])
		}
	}
	slices.Sort(names)
	return names
}

func podmanStackExists(h *harness) {
	h.existingStack(colocatedSettings(), testImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
}

func TestUninstallRemovesTheContainersAndKeepsTheState(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	h.writeFiles()
	if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ContainerServe, ContainerMigrate, ContainerDB} {
		if len(h.f.find("rm --force --ignore --time 45 "+name)) == 0 {
			t.Errorf("%s was not removed", name)
		}
	}
	for _, m := range h.mutations() {
		if !strings.HasPrefix(m, "rm ") {
			t.Errorf("uninstall without --purge did more than remove containers: %s", m)
		}
	}
	if !exists(h.settingsPath()) || !exists(h.tokenPath()) {
		t.Error("the settings or the token copy were removed without --purge")
	}
}

func TestUninstallPurgeRemovesTheStateToo(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	h.writeFiles()
	if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--purge", "--yes"); err != nil {
		t.Fatal(err)
	}
	if got, want := h.removed("volume rm"), []string{VolumeData, VolumeDBData}; !slices.Equal(got, want) {
		t.Errorf("volumes removed: %v, want %v", got, want)
	}
	if got := h.removed("network rm"); !slices.Equal(got, []string{NetworkName}) {
		t.Errorf("networks removed: %v", got)
	}
	secrets := h.removed("secret rm --ignore")
	for _, s := range secretCatalog {
		if !slices.Contains(secrets, s.name) {
			t.Errorf("the secret %s was not removed: %v", s.name, secrets)
		}
	}
	if exists(h.settingsPath()) || exists(h.tokenPath()) {
		t.Error("the settings or the token copy are still there")
	}
}

func TestUninstallPurgeOfDockerRemovesTheSecretVolumes(t *testing.T) {
	h := dockerHarness(t, "windows")
	s := DefaultSettings()
	s.Runtime = RuntimeDocker
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	h.have(KindContainer, ContainerDB, ContainerServe)
	h.have(KindNetwork, NetworkName)
	secretVolumes := []string{"suiteward-secrets-suiteward", "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate"}
	h.have(KindVolume, append([]string{VolumeData, VolumeDBData}, secretVolumes...)...)
	if err := os.WriteFile(h.tokenPath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.uninstall("--runtime", "docker", "--purge", "--yes"); err != nil {
		t.Fatal(err)
	}
	want := append([]string{VolumeData, VolumeDBData}, secretVolumes...)
	slices.Sort(want)
	if got := h.removed("volume rm"); !slices.Equal(got, want) {
		t.Errorf("volumes removed: %v, want %v", got, want)
	}
	if len(h.f.find("container remove")) == 0 && len(h.f.find("rm --force suiteward")) == 0 {
		t.Errorf("the containers were not removed: %v", h.f.lines())
	}
	if exists(h.settingsPath()) || exists(h.tokenPath()) {
		t.Error("the settings or the token copy are still there")
	}
}

func TestUninstallAsksBeforeRemovingAnything(t *testing.T) {
	h := newHarness(t, "linux")
	podmanStackExists(h)
	err := h.uninstall("--runtime", "podman", "--connection", "pm", "--purge")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()

	h = newHarness(t, "linux")
	podmanStackExists(h)
	env := h.newEnv()
	env.Term = Terminal{
		In: strings.NewReader("yes\n"), Out: &h.out, IsTerminal: true,
		ReadPassword: func() ([]byte, error) { return nil, errors.New("no secret is asked for") },
	}
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}
	if err := Uninstall(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if len(h.f.find("rm --force --ignore --time 45 "+ContainerServe)) == 0 {
		t.Error("nothing was removed after the answer yes")
	}
	// A refusal at the prompt removes nothing.
	h = newHarness(t, "linux")
	podmanStackExists(h)
	env = h.newEnv()
	env.Term = Terminal{In: strings.NewReader("n\n"), Out: &h.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
	if err := Uninstall(context.Background(), args, env); err == nil {
		t.Error("an answer of no was not refused")
	}
	h.mustNotMutate()
}

func TestUninstallNeverTouchesAnotherInstance(t *testing.T) {
	// The default settings belong to the other runtime.
	useDefaultConfigDir(t)
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	other := DefaultSettings()
	other.Runtime = RuntimeDocker
	if err := SaveSettings(path, other); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "linux")
	podmanStackExists(h)
	err = Uninstall(context.Background(), []string{"--runtime", "podman", "--connection", "pm", "--purge", "--yes", "--token-file", h.tokenPath()}, h.newEnv())
	if err == nil || !strings.Contains(err.Error(), "--settings") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
	if !exists(path) {
		t.Error("the settings of the other instance were removed")
	}

	// A Docker container nobody made through the installer.
	h = dockerHarness(t, "windows")
	h.have(KindContainer, ContainerServe)
	if err := h.uninstall("--runtime", "docker", "--yes"); !errors.Is(err, ErrForeignContainer) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()

	// A Podman stack that was edited.
	h = newHarness(t, "linux")
	h.existingStack(colocatedSettings(), testImage, func(name string, obj map[string]any) {
		if name == ContainerServe {
			obj["HostConfig"].(map[string]any)["Memory"] = int64(512 << 20)
		}
	})
	var diff *DiffError
	if err := h.uninstall("--runtime", "podman", "--connection", "pm", "--yes", "--purge"); !errors.As(err, &diff) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestUninstallOfTheIsolatedTierNeedsTheServiceAccount(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.uninstall("--tier", "isolated", "--yes")
	if err == nil || !strings.Contains(err.Error(), ServiceAccount) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestUninstallUsageError(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.uninstall("--no-such-flag"); !errors.Is(err, ErrUsage) {
		t.Fatalf("got %v", err)
	}
}

// ---- the commands of the binary ----

func (h *harness) main(args ...string) (int, string) {
	h.t.Helper()
	var stderr bytes.Buffer
	code := Main(context.Background(), args, h.newEnv(), &stderr)
	return code, stderr.String()
}

func TestMainDispatchesTheCommands(t *testing.T) {
	h := newHarness(t, "windows")
	if code, stderr := h.main("install", "host", "--yes"); code != 1 || !strings.Contains(stderr, "AlmaLinux") {
		t.Errorf("install host: %d %q", code, stderr)
	}
	if code, stderr := h.main("uninstall", "--bogus"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("uninstall: %d %q", code, stderr)
	}
	if code, stderr := h.main("bogus"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("unknown: %d %q", code, stderr)
	}
	if code, stderr := h.main("doctor", "--bogus"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("doctor: %d %q", code, stderr)
	}
}

func TestMainDoctorReadsTheSettingsFlag(t *testing.T) {
	h := newHarness(t, "linux")
	missing := filepath.Join(h.dir, "no-such-settings.yaml")
	code, _ := h.main("doctor", "--settings", missing)
	if code != 1 || !strings.Contains(h.out.String(), "settings") || !strings.Contains(h.out.String(), "FAIL") {
		t.Fatalf("exit %d, output:\n%s", code, h.out.String())
	}
	h.out.Reset()
	if code, _ := h.main("doctor", "--json", "--settings", missing); code != 1 || !strings.HasPrefix(strings.TrimSpace(h.out.String()), "[") {
		t.Fatalf("--json: exit %d, output:\n%s", code, h.out.String())
	}
}

func TestMainInstallStillInstalls(t *testing.T) {
	h := newHarness(t, "linux")
	code, stderr := h.main("install", "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--image", testImage)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// ---- the image of the release binary ----

func TestInstallDefaultsTheImageToTheReleaseTheBinaryCarries(t *testing.T) {
	was := ImageDigest
	ImageDigest = testDigest
	t.Cleanup(func() { ImageDigest = was })
	h := newHarness(t, "linux")
	if err := h.install("--runtime", "podman", "--connection", "pm"); err != nil {
		t.Fatalf("no --image on a release binary: %v", err)
	}
	if run := h.f.find("--name suiteward --network"); len(run) != 1 || !strings.HasSuffix(run[0].line(), " "+testImage) {
		t.Errorf("the service did not start from the embedded release: %v", run)
	}
	if got := h.settings().Image; got != testImage {
		t.Errorf("saved image %q", got)
	}
	// An installed image is kept by a plain re-run, and --image still wins.
	h2 := newHarness(t, "linux")
	other := "ghcr.io/ignisdevne/suiteward@sha256:" + strings.Repeat("c", 64)
	if err := h2.install("--runtime", "podman", "--connection", "pm", "--image", other); err != nil {
		t.Fatal(err)
	}
	if got := h2.settings().Image; got != other {
		t.Errorf("--image did not win over the embedded digest: %q", got)
	}
}

func TestInstallOfADevelopmentBuildNeedsTheImage(t *testing.T) {
	was := ImageDigest
	ImageDigest = ""
	t.Cleanup(func() { ImageDigest = was })
	h := newHarness(t, "linux")
	if err := h.install("--runtime", "podman", "--connection", "pm"); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}
