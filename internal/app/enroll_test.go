package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// enroll runs before the settings are loaded, like the other commands of the machine that installs: Env{} has no SUITEWARD_*
// settings, and a command that loaded them would fail on those instead of on the flag.
func TestEnrollIsDispatchedBeforeTheSettingsAreLoadedAndIsInTheUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), []string{"enroll", "--no-such-flag"}, Env{}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "no-such-flag") || !strings.Contains(errOut.String(), "suiteward enroll") {
		t.Errorf("Run(enroll --no-such-flag) = %d, stderr %q; want 2 and the flag named in the installer's usage", code, errOut.String())
	}
	errOut.Reset()
	Run(context.Background(), nil, Env{Lookup: func(string) (string, bool) { return "", false }}, &out, &errOut)
	if !strings.Contains(errOut.String(), "suiteward enroll [--owner LOGIN]") {
		t.Errorf("the usage of the binary lacks the enroll verb: %q", errOut.String())
	}
}
