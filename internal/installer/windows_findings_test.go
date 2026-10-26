package installer

import (
	"strings"
	"testing"
	"time"
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

// WIN-1: the question of the hidden paste is one clause: the screen adds "(input is hidden)", so the label has no parenthesis of its own.
func TestTheHiddenPasteQuestionIsOneClauseWithOneParenthesis(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := newHiddenReader(t)
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 40 * time.Millisecond
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			hidden.readsReach(t, 1, out)
			want := "No answer from GitHub yet. Paste the address, or press Enter if the browser says SuiteWard received the answer (input is hidden): "
			if !shows(out.String(), want) {
				t.Errorf("the question is not %q:\n%s", want, out.String())
			}
			if strings.Contains(flat(out.String()), ") (") {
				t.Errorf("the question has two parentheses in a row:\n%s", out.String())
			}
			callback(t, base, "browsercode", stateOf(t, action))
			hidden.send("")
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "browsercode" {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}
