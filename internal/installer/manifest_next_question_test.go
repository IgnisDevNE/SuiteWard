package installer

import (
	"io"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// WIN-7: the run of the live test on Windows went from the wait for the installation straight to the owner page, as if the multiple choice of
// the repositories had been answered with an Enter that nobody pressed. The hidden paste of the manifest code, which was open when the App
// callback came, was suspected of leaving a read or a key behind for the next question. It does not: an Enter ends the paste (a callback does
// not end it, which the question says: "or press Enter if the browser says SuiteWard received the answer"), and the question that follows
// waits for a key of its own. What can answer it with no key at the time is an Enter that is already queued: a line feed that follows the
// carriage return of the paste in a read of its own, or a key pressed while no question was reading (the wait for the installation).

func TestTheQuestionAfterTheManifestPasteWaitsForAKeyOfItsOwn(t *testing.T) {
	for name, enter := range map[string]string{"a carriage return": "\r", "a carriage return and a line feed together": "\r\n"} {
		t.Run(name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			env, out, keys := keyedManifestEnv(t, liveContext(t), pr, false)
			done := startManifestWith(liveContext(t), env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			waitForText(t, out, "No answer from GitHub yet")
			callback(t, base, "latecode1", stateOf(t, action))
			select {
			case res := <-done:
				t.Fatalf("the paste ended with the callback, with no key: %+v", res)
			case <-time.After(200 * time.Millisecond): // the callback does not end the paste: the Enter of the user does
			}
			go func() { _, _ = io.WriteString(pw, enter) }()
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "latecode1" {
				t.Fatalf("result = %+v", res)
			}
			keys.balanced(t)

			type answer struct {
				picked []string
				err    error
			}
			asked := make(chan answer, 1)
			go func() {
				picked, err := env.R.MultiSelect("repositories", ui.Multi{Label: "Repositories to protect", Choices: []ui.Choice{{Name: "acme/api"}, {Name: "acme/web"}}, Defaults: []int{0, 1}, Min: 1, Max: 2, Hint: "numbers"})
				asked <- answer{picked, err}
			}()
			select {
			case a := <-asked:
				t.Fatalf("the multiple choice answered itself with no key: %+v", a)
			case <-time.After(200 * time.Millisecond):
			}
			go func() { _, _ = io.WriteString(pw, "\r") }()
			select {
			case a := <-asked:
				if a.err != nil || !slices.Equal(a.picked, []string{"acme/api", "acme/web"}) {
					t.Errorf("the multiple choice = %+v, want its defaults", a)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the multiple choice did not take its Enter")
			}
		})
	}
}
