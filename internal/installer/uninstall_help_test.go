package installer

import (
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

// --purge removes the MCP token copy only when it is this instance's: the file --token-file names, or the default token file with the default
// settings file (see tokenFileOf). The usage of the flag says so, and never promises the removal of a file that a purge keeps.
func TestThePurgeUsageSaysWhichTokenFileItRemoves(t *testing.T) {
	h := newHarness(t, "linux")
	if err := Uninstall(context.Background(), []string{"--help"}, h.newEnv()); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Uninstall --help = %v, want the end of the help", err)
	}
	help := strings.Join(strings.Fields(h.out.String()), " ")
	if strings.Contains(help, "the access token copy and the settings file") {
		t.Errorf("the usage of --purge says that it removes the MCP token copy whatever it is:\n%s", h.out.String())
	}
	for _, want := range []string{"the access token copy only when it is this instance's", "--token-file", "the default settings file"} {
		if !strings.Contains(help, want) {
			t.Errorf("the usage of --purge lacks %q:\n%s", want, h.out.String())
		}
	}
}
