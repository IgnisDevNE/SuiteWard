package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// The list is a file in the configuration folder that anyone who can write there can edit, and what it holds is printed and acted on: an entry that
// cannot be right is refused as a list that cannot be read is, and nothing from the list reaches the terminal as it is.

// validEntry is an entry that an install could have written.
func validEntry(t *testing.T) installation {
	t.Helper()
	return installation{
		Settings: absolute(filepath.Join(t.TempDir(), "settings.yaml")), TokenFile: tokenName("valid"), Tier: config.TierCoLocated, Runtime: RuntimePodman,
		Connection: "pm", Instance: testInstance, HTTPPort: 27081, MCPPort: 27091,
	}
}

// writeList writes a list of version 1 that holds the entries, as a person or a program of another kind could have.
func writeList(t *testing.T, entries ...installation) string {
	t.Helper()
	path := listFile(t)
	raw, err := yaml.Marshal(installationList{Version: installationsVersion, Installations: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAListEntryThatCannotBeRightIsRefusedLikeAListThatCannotBeRead(t *testing.T) {
	for name, bad := range map[string]func(e *installation){
		"a settings file that is not absolute":    func(e *installation) { e.Settings = "relative/settings.yaml" },
		"a token file that is not absolute":       func(e *installation) { e.TokenFile = "relative/mcp-token" },
		"a tier that is not one":                  func(e *installation) { e.Tier = "cloud" },
		"a runtime that is not one":               func(e *installation) { e.Runtime = "nerdctl" },
		"a connection that is not a plain name":   func(e *installation) { e.Connection = "-rm -rf" },
		"an instance id that is not 8 hex digits": func(e *installation) { e.Instance = "1A2B3C4D5" },
		"a port that is zero":                     func(e *installation) { e.HTTPPort = 0 },
		"a port that is too high":                 func(e *installation) { e.MCPPort = 70000 },
		"a control character in the settings":     func(e *installation) { e.Settings += "\x1b[31m" },
		"a line break in the token file":          func(e *installation) { e.TokenFile += "\nfake" },
	} {
		t.Run(name, func(t *testing.T) {
			useDefaultConfigDir(t)
			e := validEntry(t)
			bad(&e)
			path := writeList(t, validEntry(t), e)
			before := readFile(t, path)
			err := recordInstallation(runtime.GOOS, settingsOf(t, colocatedSettings()), "", colocatedSettings(), "")
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "entry 2") {
				t.Errorf("recordInstallation = %v, want a refusal that names %s and the entry", err, path)
			}
			if _, _, err := installationCandidates(runtime.GOOS); err == nil || !strings.Contains(err.Error(), "entry 2") {
				t.Errorf("installationCandidates = %v, want a refusal that names the entry", err)
			}
			if after := readFile(t, path); after != before {
				t.Errorf("the list was replaced by %q", after)
			}
			if err != nil && strings.ContainsAny(err.Error(), "\x1b\n") {
				t.Errorf("the refusal carries a control character of the list: %q", err.Error())
			}
		})
	}
	t.Run("an entry that could have been written by an install", func(t *testing.T) {
		useDefaultConfigDir(t)
		writeList(t, validEntry(t))
		if _, err := loadInstallations(listFile(t)); err != nil {
			t.Errorf("a valid list was refused: %v", err)
		}
	})
}

// An empty file is not a list of another version.
func TestAnEmptyListFileSaysItIsEmpty(t *testing.T) {
	for name, content := range map[string]string{"nothing": "", "blank lines": "\n  \n"} {
		useDefaultConfigDir(t)
		path := listFile(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadInstallations(path)
		if err == nil || !strings.Contains(err.Error(), "is empty") || strings.Contains(err.Error(), "version 0") || strings.Contains(err.Error(), "another release") {
			t.Errorf("%s: %v, want a refusal that says the file is empty and not that another release wrote it", name, err)
		}
	}
}

// A run that cannot read the list says what to do, and what deleting it costs.
func TestARefusedListSaysWhatToDo(t *testing.T) {
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	bad := validEntry(t)
	bad.Tier = "cloud"
	writeList(t, bad)
	term, _ := fakeTerminal(true, "")
	r := newTestResolver(t, nil, nil, term)
	err := r.chooseInstallation(choiceDeps(t))
	for _, want := range []string{"--settings <file>", "--token-file <file>", "fix or delete that list", "Each installation adds its own entry again the next time it is installed or upgraded"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is %v, want it to say %q", err, want)
		}
	}
}

// What comes out of the list is printed as one line of text: no control character reaches the terminal, in the row of an installation or in the lines of
// a plain screen.
func TestNothingOfTheListReachesTheTerminalAsItIs(t *testing.T) {
	hostile := candidate{installation: installation{
		Settings: absolute("/x/\x1b[31mred\nfake line/settings.yaml"), Tier: config.TierCoLocated, Runtime: "podman\x1b[0m", Connection: "pm\r2", Instance: "ab\tcd", HTTPPort: 1, MCPPort: 2,
	}}
	if got := hostile.describe(); strings.ContainsAny(got, "\x1b\n\r\t") {
		t.Errorf("a row says %q, which holds a control character", got)
	}
	term, out := fakeTerminal(true, "x\n")
	r := newTestResolver(t, nil, nil, term)
	_, _ = r.pickInstallation(choiceDeps(t), []candidate{hostile, hostile}, 0) // the answer is not one of them: only what was printed matters
	if strings.ContainsAny(out.String(), "\x1b\r\t") || strings.Contains(out.String(), "\nfake line") {
		t.Errorf("a plain screen printed %q, which holds a control character or a line that the list made", out.String())
	}
}
