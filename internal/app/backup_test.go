package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// backup and restore are commands of the machine that installs, like enroll: they run before the settings are loaded, and Env{} has no
// SUITEWARD_* settings, so a command that loaded them would fail on those instead of on the flag.
func TestBackupAndRestoreAreDispatchedBeforeTheSettingsAreLoaded(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		usage string // a line of the usage of the installer, which Main prints with a usage error
	}{
		{[]string{"backup", "--no-such-flag"}, "suiteward backup ["},
		{[]string{"backup", "init", "--no-such-flag"}, "suiteward backup init"},
		{[]string{"restore", "--no-such-flag"}, "suiteward restore <backup.tar.age>"},
	} {
		var out, errOut bytes.Buffer
		code := Run(context.Background(), tc.args, Env{}, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), "no-such-flag") || !strings.Contains(errOut.String(), tc.usage) {
			t.Errorf("Run(%v) = %d, stderr %q; want 2, the flag named and %q in the installer's usage", tc.args, code, errOut.String(), tc.usage)
		}
	}
}

// The title bar of the installer's full screen shows the build version: Run hands it over.
func TestTheInstallerCommandsGetTheBuildVersion(t *testing.T) {
	if got := installerEnv(io.Discard).Version; got != Version {
		t.Errorf("the installer's version is %q, want the build version %q", got, Version)
	}
}
