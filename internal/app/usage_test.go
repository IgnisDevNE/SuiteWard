package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer"
)

// The installer's commands are dispatched by this binary, so its usage lists every line that the installer's own usage lists: a command
// added to one and not to the other (backup, restore) would be a command a user cannot find.
func TestTheUsageOfTheBinaryListsEveryCommandLineOfTheInstaller(t *testing.T) {
	var out, errOut bytes.Buffer
	Run(context.Background(), nil, Env{Lookup: func(string) (string, bool) { return "", false }}, &out, &errOut)
	for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(installer.Usage, "usage:")), "\n") {
		if line = strings.TrimSpace(line); !strings.Contains(errOut.String(), line) {
			t.Errorf("the usage of the binary lacks %q:\n%s", line, errOut.String())
		}
	}
}
