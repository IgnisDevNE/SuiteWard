package installer

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The installer waits for GitHub's callback without reading the terminal: a read that is still pending when the callback arrives would
// take the answer to the next question (the Resolver's reader is shared and cannot be given up), or ask the user to press Enter to end
// it. A user whose browser cannot reach the installer is offered the paste once the callback has not come for a while, as a hidden
// read: the code is a credential, and neither the terminal nor the frame may show it.

// hiddenReader is the no-echo reader of a test: it serves the lines it is sent, one per read, and the end of the input once the test
// is over.
type hiddenReader struct {
	lines chan string
	reads atomic.Int32
}

func newHiddenReader(t *testing.T) *hiddenReader {
	h := &hiddenReader{lines: make(chan string, 8)}
	t.Cleanup(func() { close(h.lines) })
	return h
}

func (h *hiddenReader) read() ([]byte, error) {
	h.reads.Add(1)
	line, ok := <-h.lines
	if !ok {
		return nil, io.EOF
	}
	return []byte(line), nil
}

func (h *hiddenReader) send(line string) { h.lines <- line }

// eof ends the input: the next read, and every one after it, fails with io.EOF.
func (h *hiddenReader) eof() { close(h.lines) }

// readsReach waits until the hidden reader has been asked n times.
func (h *hiddenReader) readsReach(t *testing.T, n int32, out *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.reads.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the hidden question was asked %d times, want %d; output:\n%s", h.reads.Load(), n, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fullManifestEnvWith is the environment of the manifest step on a full screen with a context that can be cancelled, as a command has
// it; readPassword answers a hidden question.
func fullManifestEnvWith(t *testing.T, in io.Reader, readPassword func() ([]byte, error)) (githubEnv, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	br := bufio.NewReader(in)
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: liveContext(t), Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	r, err := NewResolver(map[string]string{}, map[string]string{}, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: readPassword})
	if err != nil {
		t.Fatal(err)
	}
	return githubEnv{R: r, Out: scr, WebURL: "https://github.example", Tier: config.TierCoLocated, ManifestWait: 5 * time.Second, PasteAfter: time.Hour}, out
}

// manifestModes are the two ways a terminal is driven: plain lines, and the full screen.
var manifestModes = []struct {
	name string
	env  func(t *testing.T, in io.Reader, readPassword func() ([]byte, error)) (githubEnv, *syncBuffer)
}{
	{"plain terminal", func(t *testing.T, in io.Reader, read func() ([]byte, error)) (githubEnv, *syncBuffer) {
		return manifestEnvWith(t, in, true, read)
	}},
	{"full screen", fullManifestEnvWith},
}

// awaitFor is await with a short wait, for a result that a flow that reads the terminal would hold back.
func awaitFor(t *testing.T, done <-chan codeResult, wait time.Duration) codeResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(wait):
		t.Fatal("obtainManifestCode did not return")
		return codeResult{}
	}
}

func TestManifestContinuesOnItsOwnWhenTheCallbackComes(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			pr, pw := io.Pipe() // nobody types: any read of it would hold the flow back
			t.Cleanup(func() { _ = pw.Close() })
			hidden := newHiddenReader(t)
			env, out := mode.env(t, pr, hidden.read)
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			callback(t, base, "browsercode", stateOf(t, action))
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "browsercode" {
				t.Fatalf("result = %+v\n%s", res, out.String())
			}
			if strings.Contains(out.String(), "Press Enter") || strings.Contains(out.String(), "browsercode") {
				t.Errorf("the user was asked for an Enter, or the code was drawn:\n%s", out.String())
			}
			if n := hidden.reads.Load(); n != 0 {
				t.Errorf("the terminal was asked a hidden question %d times before anyone was late", n)
			}

			// What the user types next answers the next question: no read of the flow is left to take it.
			go func() { _, _ = io.WriteString(pw, "the next answer\n") }()
			got, err := env.R.Value("next", "Next question", "")
			if err != nil || got != "the next answer" {
				t.Fatalf("the next question got %q, %v", got, err)
			}
		})
	}
}

func TestManifestOffersThePasteOnlyWhenTheCallbackIsLate(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := newHiddenReader(t)
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 80 * time.Millisecond
			done := startManifest(env, "acme")
			waitForListener(t, out)
			hidden.readsReach(t, 1, out)
			if !shows(out.String(), "No answer from GitHub yet") {
				t.Errorf("the offer does not say why it is made:\n%s", out.String())
			}
			hidden.send("pastedcode")
			res := awaitFor(t, done, 3*time.Second)
			if res.err != nil || res.code != "pastedcode" {
				t.Fatalf("result = %+v", res)
			}
			if strings.Contains(out.String(), "pastedcode") {
				t.Errorf("the pasted code was drawn:\n%s", out.String())
			}
		})
	}
}

func TestManifestPastedAddressIsCheckedAgainstTheState(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := newHiddenReader(t)
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 40 * time.Millisecond
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			hidden.readsReach(t, 1, out)
			hidden.send(base + "callback?code=urlcode1&state=other")
			hidden.readsReach(t, 2, out) // refused, and asked again
			if !strings.Contains(out.String(), "That was refused") {
				t.Errorf("the refusal was not said:\n%s", out.String())
			}
			hidden.send(base + "callback?code=urlcode1&state=" + stateOf(t, action))
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "urlcode1" {
				t.Fatalf("result = %+v", res)
			}
			if strings.Contains(out.String(), "urlcode1") {
				t.Errorf("the code of the pasted address was drawn:\n%s", out.String())
			}
		})
	}
}

// An Enter with nothing pasted keeps waiting, and takes the callback that came while the question was open.
func TestManifestEnterKeepsWaitingAndTakesALateCallback(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := newHiddenReader(t)
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 40 * time.Millisecond
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			hidden.readsReach(t, 1, out)
			callback(t, base, "latecode1", stateOf(t, action))
			hidden.send("") // the user is back at the terminal
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "latecode1" {
				t.Fatalf("result = %+v\n%s", res, out.String())
			}
		})
	}
}

func TestManifestEndOfInputLeavesTheCallbackToDoIt(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := &hiddenReader{lines: make(chan string)}
			hidden.eof()
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 40 * time.Millisecond
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			hidden.readsReach(t, 1, out)
			callback(t, base, "browsercode", stateOf(t, action))
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "browsercode" {
				t.Fatalf("result = %+v\n%s", res, out.String())
			}
			if n := hidden.reads.Load(); n != 1 {
				t.Errorf("a closed input was read %d times", n)
			}
		})
	}
}

// Nothing is asked where nothing can be answered.
func TestManifestOffersNoPasteWithoutATerminal(t *testing.T) {
	hidden := newHiddenReader(t)
	env, out := manifestEnvWith(t, strings.NewReader(""), false, hidden.read)
	env.PasteAfter = 20 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	time.Sleep(150 * time.Millisecond)
	callback(t, base, "browsercode", stateOf(t, action))
	if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "browsercode" {
		t.Fatalf("result = %+v", res)
	}
	if n := hidden.reads.Load(); n != 0 {
		t.Errorf("a run without a terminal was asked %d hidden questions", n)
	}
}

func TestManifestAnswersFileCanCarryTheCode(t *testing.T) {
	env, _ := manifestEnv(t, strings.NewReader(""), false)
	env.R.answers["manifest-code"] = "fromanswers"
	if res := await(t, startManifest(env, "acme")); res.err != nil || res.code != "fromanswers" {
		t.Fatalf("result = %+v", res)
	}
}

var appNamePattern = regexp.MustCompile(`suiteward-[a-z0-9]{6}`)

// A full screen explains the step on a page of its own, with the address and the name of the App; a plain run prints what it printed.
func TestManifestPageExplainsTheStepOnAFullScreen(t *testing.T) {
	env, out := fullManifestEnvWith(t, strings.NewReader(""), func() ([]byte, error) { return nil, errors.New("no password here") })
	env.ManifestWait = 300 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	action, manifest := fetchForm(t, base)
	name := checkManifest(t, base, manifest)
	_ = action
	text := out.String()
	for _, want := range []string{"Create the GitHub App", "approve", "by itself", name} {
		if !shows(text, want) {
			t.Errorf("the page lacks %q:\n%s", want, text)
		}
	}
	if shows(text, "ssm") {
		t.Errorf("a co-located install showed a port-forward hint:\n%s", text)
	}
	await(t, done)
}

func TestManifestPageOfTheIsolatedTierHasThePortForward(t *testing.T) {
	env, out := fullManifestEnvWith(t, strings.NewReader(""), func() ([]byte, error) { return nil, errors.New("no password here") })
	env.Tier = config.TierIsolated
	env.ManifestWait = 300 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	port := strings.TrimSuffix(strings.TrimPrefix(base, "http://127.0.0.1:"), "/")
	for _, want := range []string{"AWS-StartPortForwardingSession", `"portNumber":["` + port + `"]`} {
		if !shows(out.String(), want) {
			t.Errorf("the page lacks %q:\n%s", want, out.String())
		}
	}
	await(t, done)
}

func TestManifestPlainOutputKeepsItsInstructions(t *testing.T) {
	env, out := manifestEnv(t, strings.NewReader(""), false)
	env.ManifestWait = 100 * time.Millisecond
	done := startManifest(env, "acme")
	waitForListener(t, out)
	await(t, done)
	text := out.String()
	name := appNamePattern.FindString(text)
	want := "Create the GitHub App: open this address in the browser of this machine and approve the App on GitHub. The App is named " + name + " unless you rename it there; note the name, in case the installer cannot finish and the App has to be deleted.\n  http://"
	if name == "" || !strings.Contains(text, want) {
		t.Errorf("the instructions of a plain run changed:\n%s", text)
	}
	if strings.Contains(text, "No answer from GitHub yet") {
		t.Errorf("a run without a terminal offered the paste:\n%s", text)
	}
}
