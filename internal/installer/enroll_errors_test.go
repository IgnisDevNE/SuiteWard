package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingReader is a terminal whose input cannot be read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the terminal went away") }

func TestEnrollRefusesWhatItCannotRunWith(t *testing.T) {
	withTerminal(t, true)
	if err := Enroll(context.Background(), nil, Env{}); err == nil {
		t.Error("an environment without commands was accepted")
	}
	h := newHarness(t, "linux")
	env := h.newEnv()
	env.Term = Terminal{} // no input, no output
	if err := Enroll(context.Background(), []string{"--settings", h.settingsPath()}, env); err == nil || !strings.Contains(err.Error(), "console") {
		t.Errorf("got %v, want the refusal of a console that cannot read", err)
	}
}

func TestEnrollRefusesAnUnknownTierAndASettingsFileItCannotRead(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice", "--tier", "nowhere"); code != 1 || !strings.Contains(stderr, "nowhere") {
		t.Errorf("tier: exit %d, stderr %q", code, stderr)
	}
	if err := os.WriteFile(h.settingsPath(), []byte("tier: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "settings") {
		t.Errorf("settings: exit %d, stderr %q", code, stderr)
	}
	if len(h.f.tty) != 0 {
		t.Error("setup ran")
	}
}

func TestEnrollNeedsTheDefaultSettingsFileToBeLocatable(t *testing.T) {
	withTerminal(t, true)
	for _, key := range []string{"APPDATA", "HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, "")
	}
	h := newHarness(t, "linux")
	env := h.newEnv()
	term, _ := fakeTerminal(true, "")
	env.Term = term
	var stderr bytes.Buffer
	if code := Main(context.Background(), []string{"enroll", "--owner", "alice"}, env, &stderr); code != 1 || !strings.Contains(stderr.String(), "configuration directory") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestEnrollRefusesAStackItCannotAdoptAndAnEngineItCannotUse(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	h.first("info --format {{.Host.Security.Rootless}}", "false\n")
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "rootful") {
		t.Errorf("a rootful connection: exit %d, stderr %q", code, stderr)
	}

	// A container that was edited is left alone, as every command of the installer leaves it.
	h = newHarness(t, "linux")
	withTerminal(t, true)
	s := localPS1Settings()
	s.Connection = "pm"
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	h.existingStack(s, testImage, func(name string, obj map[string]any) {
		if name == ContainerServe {
			obj["HostConfig"].(map[string]any)["Memory"] = int64(512 << 20)
		}
	})
	h.status = statusIs(statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "memory") || len(h.f.tty) != 0 {
		t.Errorf("an edited stack: exit %d, stderr %q", code, stderr)
	}
}

func TestEnrollRefusesTheDefaultSettingsOfAnotherConnection(t *testing.T) {
	withTerminal(t, true)
	useDefaultConfigDir(t)
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, DefaultSettings()); err != nil { // an instance with no connection of its own
		t.Fatal(err)
	}
	h := newHarness(t, "windows")
	h.f.on("system connection list", machineConnections)
	env := h.newEnv()
	term, _ := fakeTerminal(true, "")
	term.Out = &h.out
	env.Term = term
	var stderr bytes.Buffer
	if code := Main(context.Background(), []string{"enroll", "--owner", "alice"}, env, &stderr); code != 1 || !strings.Contains(stderr.String(), "--settings") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestEnrollRefusesAStatusThatIsNotJSONAndAConfirmationItCannotRead(t *testing.T) {
	h := enrollHarness(t, "<html>not the service</html>")
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "not JSON") || len(h.f.tty) != 0 {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	h = enrollHarness(t, statusEnrolled)
	term, _ := fakeTerminal(true, "")
	term.In = failingReader{}
	if code, stderr := h.enrollOn(term, "--owner", "alice"); code != 1 || !strings.Contains(stderr, "read the confirmation") || len(h.f.tty) != 0 {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	// The end of the input is a no, not a yes.
	h = enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "not confirmed") || len(h.f.tty) != 0 {
		t.Errorf("end of input: exit %d, stderr %q", code, stderr)
	}
}

func TestEnrollOffersNoLoginWhenStatusHasNone(t *testing.T) {
	h := enrollHarness(t, `{"version":"v0.2.0","owner":{"login":""}}`)
	if code, stderr := h.enroll("enroll\n\n"); code != 1 || !strings.Contains(stderr, "owner") || len(h.f.tty) != 0 { // Enter takes no default: there is none
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	h = enrollHarness(t, `{"version":"v0.2.0","owner":{"login":""}}`)
	if code, stderr := h.enroll("enroll\ncarol\n"); code != 0 || !strings.Contains(h.setupArgs(), "setup --owner carol ") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestEnrollRefusesATestCheckListThatIsNotOne(t *testing.T) {
	h := enrollHarness(t, statusNoOwner)
	if code, stderr := h.enroll("", "--owner", "alice", "--test-check", "Acme/api=a", "--test-check", "acme/API=b"); code != 1 || !strings.Contains(stderr, "twice") {
		t.Errorf("a repository twice: exit %d, stderr %q", code, stderr)
	}
	if code, stderr := h.enroll("", "--owner", "alice", "--test-check", "CI / test"); code != 1 || !strings.Contains(stderr, "OWNER/NAME=CHECK") {
		t.Errorf("a plain name: exit %d, stderr %q", code, stderr)
	}
	// A repository without a check that cannot be asked: the input ended.
	h = enrollHarnessWith(t, statusNoOwner, func(s *Settings) { s.GitHub.TestChecks = nil })
	if code, stderr := h.enroll("", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "Acme/api") || len(h.f.tty) != 0 {
		t.Errorf("no check: exit %d, stderr %q", code, stderr)
	}
}

func TestEnrollIsolatedRefusesWhatItCannotAdoptOrRead(t *testing.T) {
	e := newIsoEnroll(t)
	path := filepath.Join(unitDir(e.f.home), "suiteward-db.container")
	edited := append(readFileOrFail(t, path), []byte("PodmanArgs=--privileged\n")...)
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stderr := e.enroll(statusIs(statusNoOwner), "", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "privileged") || len(e.f.tty) != 0 {
		t.Errorf("an edited unit: exit %d, stderr %q", code, stderr)
	}

	e = newIsoEnroll(t)
	s := isolatedSettings()
	s.HTTPPort, s.MCPPort, s.JobTimeout, s.Image = 18081, 18091, "3m", oldImage
	if err := SaveSettings(settingsFile(e.f.home), s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile(e.f.home), []byte("SUITEWARD_GITHUB_APP_ID=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stderr := e.enroll(statusIs(statusNoOwner), "", "--owner", "alice"); code != 1 || !strings.Contains(stderr, "github.env") || len(e.f.tty) != 0 {
		t.Errorf("a broken env file: exit %d, stderr %q", code, stderr)
	}
}
