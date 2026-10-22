package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// install runs before the settings are loaded: the host that installs has no SUITEWARD_* environment, and an Env without a
// lookup function would fail the moment the settings were read.
func TestInstallIsDispatchedBeforeTheSettingsAreLoaded(t *testing.T) {
	var out, errOut bytes.Buffer
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	code := Run(context.Background(), []string{"install", "--settings", settings, "--runtime", "nerdctl", "--image", "x"}, Env{}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "runtime") {
		t.Fatalf("Run(install --runtime nerdctl) = %d, stderr %q; want 1 and a message about the runtime", code, errOut.String())
	}
}

func TestInstallUsageErrorsExitWithTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"install", "--no-such-flag"}, Env{}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "usage") {
		t.Fatalf("Run(install --no-such-flag) = %d, stderr %q; want 2 and a usage message", code, errOut.String())
	}
}

func TestUsageNamesInstall(t *testing.T) {
	var out, errOut bytes.Buffer
	Run(context.Background(), nil, Env{Lookup: func(string) (string, bool) { return "", false }}, &out, &errOut)
	if !strings.Contains(errOut.String(), "suiteward install") {
		t.Fatalf("usage: %q", errOut.String())
	}
}
