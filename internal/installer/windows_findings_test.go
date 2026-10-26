package installer

import (
	"strings"
	"testing"
)

// The small findings of the live run on Windows (M1.15, WIN-n): the texts and screens that read wrong.

// WIN-8: "did not see it, keep it or log it" reads as an order to keep or log the secret.
func TestTheEndOfSetupSaysTheInstallerDidNotSeeKeepOrLogTheSecret(t *testing.T) {
	scr, _, out := fullScreenOn(t, liveContext(t), strings.NewReader(""))
	t.Cleanup(func() { _ = scr.Close() })
	if err := afterSetup(scr, "suiteward", nil); err != nil {
		t.Fatal(err)
	}
	if !shows(out.String(), "the installer did not see, keep or log it") || shows(out.String(), "see it, keep it") {
		t.Errorf("the page of the end of setup is:\n%s", out.String())
	}
}
