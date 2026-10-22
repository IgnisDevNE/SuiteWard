package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func validGitHub() GitHub {
	return GitHub{
		AppID: 1234, InstallationID: 5678, Owner: "Acme",
		Repositories: []string{"Acme/api", "acme/web"},
		TestChecks:   map[string]string{"Acme/api": "CI / test"},
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suiteward", "settings.yaml")
	want := DefaultSettings()
	want.Tier, want.Runtime, want.Connection = config.TierIsolated, RuntimePodman, "machine-1"
	want.HTTPPort, want.MCPPort, want.JobTimeout = 18081, 18091, "3m"
	want.GitHub = validGitHub()
	if err := SaveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != want.Tier || got.Runtime != want.Runtime || got.Connection != want.Connection || got.HTTPPort != want.HTTPPort ||
		got.MCPPort != want.MCPPort || got.JobTimeout != want.JobTimeout || got.GitHub.AppID != want.GitHub.AppID ||
		got.GitHub.InstallationID != want.GitHub.InstallationID || got.GitHub.Owner != want.GitHub.Owner ||
		strings.Join(got.GitHub.Repositories, ",") != strings.Join(want.GitHub.Repositories, ",") ||
		got.GitHub.TestChecks["Acme/api"] != "CI / test" {
		t.Fatalf("round trip changed the settings:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveSettingsUsesRestrictiveModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes do not apply on Windows; the owner-only ACL is the co-located installer's")
	}
	dir := filepath.Join(t.TempDir(), "suiteward")
	path := filepath.Join(dir, "settings.yaml")
	if err := SaveSettings(path, DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s has mode %o, want %o", p, info.Mode().Perm(), want)
		}
	}
}

func TestLoadSettingsFillsDefaultPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("tier: isolated\nruntime: podman\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPPort != DefaultHTTPPort || got.MCPPort != DefaultMCPPort || got.Tier != config.TierIsolated {
		t.Fatalf("defaults were not applied: %+v", got)
	}
}

func TestLoadSettingsMissingFile(t *testing.T) {
	_, err := LoadSettings(filepath.Join(t.TempDir(), "nope.yaml"))
	if !errors.Is(err, ErrNoSettings) {
		t.Fatalf("got %v, want ErrNoSettings", err)
	}
}

func TestLoadSettingsRejectsUnknownKeyAndInvalidValues(t *testing.T) {
	for name, content := range map[string]string{
		"unknown key":  "tier: isolated\nruntime: podman\nhttp_prot: 1\n",
		"invalid tier": "tier: cloud\nruntime: podman\n",
	} {
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(path); err == nil || errors.Is(err, ErrNoSettings) {
			t.Errorf("%s: got %v, want a validation error", name, err)
		}
	}
}

func TestSaveSettingsRefusesInvalidSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	s := DefaultSettings()
	s.HTTPPort = s.MCPPort
	if err := SaveSettings(path, s); err == nil {
		t.Fatal("saved settings with equal ports")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an invalid save left a file behind: %v", err)
	}
}

func TestSettingsValidate(t *testing.T) {
	repos := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, "acme/r"+strings.Repeat("x", i+1))
		}
		return out
	}
	cases := []struct {
		name   string
		mutate func(*Settings)
		want   string // substring of the error, empty for valid
	}{
		{"defaults are valid", func(*Settings) {}, ""},
		{"docker co-located", func(s *Settings) { s.Runtime = RuntimeDocker }, ""},
		{"full github", func(s *Settings) { s.GitHub = validGitHub() }, ""},
		{"twenty repositories", func(s *Settings) {
			s.GitHub = validGitHub()
			s.GitHub.Repositories, s.GitHub.TestChecks = repos(20), nil
		}, ""},
		{"timeout", func(s *Settings) { s.JobTimeout = "1m30s" }, ""},
		{"unknown tier", func(s *Settings) { s.Tier = "cloud" }, "tier"},
		{"unknown runtime", func(s *Settings) { s.Runtime = "lxc" }, "runtime"},
		{"isolated is podman only", func(s *Settings) { s.Tier, s.Runtime = config.TierIsolated, RuntimeDocker }, "isolated"},
		{"connection that looks like a flag", func(s *Settings) { s.Connection = "--url=ssh://x" }, "connection"},
		{"equal ports", func(s *Settings) { s.MCPPort = s.HTTPPort }, "distinct"},
		{"port zero", func(s *Settings) { s.HTTPPort = 0 }, "http_port"},
		{"port too large", func(s *Settings) { s.MCPPort = 65536 }, "mcp_port"},
		{"timeout not a duration", func(s *Settings) { s.JobTimeout = "soon" }, "job_timeout"},
		{"timeout not positive", func(s *Settings) { s.JobTimeout = "-5s" }, "job_timeout"},
		{"timeout zero", func(s *Settings) { s.JobTimeout = "0s" }, "job_timeout"},
		{"github without repositories", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Repositories = nil }, "repositories"},
		{"twenty-one repositories", func(s *Settings) {
			s.GitHub = validGitHub()
			s.GitHub.Repositories, s.GitHub.TestChecks = repos(21), nil
		}, "at most 20"},
		{"repository without owner", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Repositories = []string{"api"} }, "owner/name"},
		{"repository dot-dot", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Repositories = []string{"acme/.."} }, "owner/name"},
		{"two owners", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Repositories = []string{"acme/api", "other/web"} }, "owner"},
		{"owner differs from the login", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Owner = "other" }, "owner"},
		{"duplicate repository", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.Repositories = []string{"acme/api", "ACME/API"} }, "twice"},
		{"github without app id", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.AppID = 0 }, "app_id"},
		{"github without installation id", func(s *Settings) { s.GitHub = validGitHub(); s.GitHub.InstallationID = 0 }, "installation_id"},
		{"test check for an unlisted repository", func(s *Settings) {
			s.GitHub = validGitHub()
			s.GitHub.TestChecks = map[string]string{"acme/other": "CI"}
		}, "test_checks"},
		{"empty test check", func(s *Settings) {
			s.GitHub = validGitHub()
			s.GitHub.TestChecks = map[string]string{"Acme/api": " "}
		}, "test_checks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mutate(&s)
			err := s.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

func TestSettingsValidateReportsEveryProblem(t *testing.T) {
	s := DefaultSettings()
	s.Tier, s.JobTimeout, s.HTTPPort = "cloud", "soon", 0
	err := s.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"tier", "job_timeout", "http_port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestDefaultSettingsPathIsPerUser(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir on this machine")
	}
	got, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "suiteward", "settings.yaml"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// The temporary file is flushed before it replaces the settings, and the directory entry after: a crash must leave the
// old file or the new one, never an empty one.
func TestWriteFileAtomicFlushesBeforeAndAfterTheRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []string
	origFile, origDir := syncFile, syncDir
	t.Cleanup(func() { syncFile, syncDir = origFile, origDir })
	syncFile = func(f *os.File) error {
		current, _ := os.ReadFile(path)
		written, _ := os.ReadFile(f.Name())
		events = append(events, fmt.Sprintf("file(old kept=%t, temp written=%t)", string(current) == "old", string(written) == "new"))
		return nil
	}
	syncDir = func(d string) error {
		current, _ := os.ReadFile(path)
		events = append(events, fmt.Sprintf("dir %s (replaced=%t)", filepath.Base(d), string(current) == "new"))
		return nil
	}
	if err := writeFileAtomic(path, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []string{"file(old kept=true, temp written=true)", "dir " + filepath.Base(dir) + " (replaced=true)"}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", events, want)
	}
}

func TestWriteFileAtomicFailsWhenTheFlushFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	orig := syncFile
	t.Cleanup(func() { syncFile = orig })
	syncFile = func(*os.File) error { return errors.New("disk full") }
	if err := writeFileAtomic(path, []byte("new"), 0o700); err == nil {
		t.Fatal("a failed flush was ignored")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file was put in place after a failed flush: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a temporary file was left behind: %v", entries)
	}
}
