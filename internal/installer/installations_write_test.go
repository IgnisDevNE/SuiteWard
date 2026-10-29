package installer

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// An entry is checked before it is written, as it is when it is read: an install whose settings file or token file has a path that the list would
// refuse does not write an entry that every later command refuses.

func TestAnEntryThatTheListWouldRefuseIsNotWritten(t *testing.T) {
	for name, c := range map[string]struct{ settings, token string }{
		"a control character in the settings file": {absolute("/x/settings\x1b[31m.yaml"), ""},
		"a line break in the token file":           {absolute("/x/settings.yaml"), absolute("/x/mcp-token\nfake")},
	} {
		t.Run(name, func(t *testing.T) {
			useDefaultConfigDir(t)
			err := recordInstallation(runtime.GOOS, c.settings, c.token, colocatedSettings(), "")
			if err == nil || !strings.Contains(err.Error(), "entry") || strings.ContainsAny(err.Error(), "\x1b\n") {
				t.Errorf("recordInstallation = %v, want a refusal that does not repeat the path", err)
			}
			if exists(listFile(t)) {
				t.Errorf("an entry that every later command would refuse was written:\n%s", readFile(t, listFile(t)))
			}
		})
	}
}

// A refused entry is the warning of a list that cannot be written, never a failure of the install, and the entries that were listed stay.
func TestAnEntryThatIsNotWrittenIsAWarning(t *testing.T) {
	useDefaultConfigDir(t)
	good := settingsOf(t, colocatedSettings())
	if err := recordInstallation(runtime.GOOS, good, tokenName("good"), colocatedSettings(), ""); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, listFile(t))
	var out bytes.Buffer
	scr, err := ui.Open(ui.Options{Out: &out, In: strings.NewReader(""), Mode: ui.ModePlain})
	if err != nil {
		t.Fatal(err)
	}
	listInstallation(scr, choiceDeps(t), absolute("/x/settings\x1b[31m.yaml"), "", colocatedSettings(), "")
	for _, want := range []string{"Could not update the list of installations", "The installation itself works"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("the warning carries a control character of the path: %q", out.String())
	}
	if after := readFile(t, listFile(t)); after != before {
		t.Errorf("the list changed:\n%s", after)
	}
	if _, err := os.Stat(listFile(t)); err != nil {
		t.Fatal(err)
	}
}
