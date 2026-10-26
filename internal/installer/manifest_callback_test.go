package installer

import (
	"bufio"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// GitHub's answer ends an open paste of the manifest code: the installer continues by itself, as the page of the step says, and the person
// at the terminal presses nothing. This holds on a terminal that can be waited on for a key without reading it (ui.Options.InputReady), where
// the end of the question leaves no read on the input; elsewhere an Enter still ends it (manifest_next_question_test.go).

// typedKeys is the keyboard of a test that a screen can wait on: what is typed waits until a read takes it.
type typedKeys struct {
	mu     sync.Mutex
	buf    []byte
	closed bool
	wake   chan struct{} // closed, and replaced, whenever something is typed or the keyboard is closed
}

func newTypedKeys(t *testing.T) *typedKeys {
	t.Helper()
	k := &typedKeys{wake: make(chan struct{})}
	t.Cleanup(func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.closed = true
		k.change()
	})
	return k
}

func (k *typedKeys) change() { close(k.wake); k.wake = make(chan struct{}) } // the caller holds the lock

func (k *typedKeys) Type(text string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.buf = append(k.buf, text...)
	k.change()
}

func (k *typedKeys) wait(done <-chan struct{}) error {
	for {
		k.mu.Lock()
		ready, wake := len(k.buf) > 0 || k.closed, k.wake
		k.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-wake:
		case <-done:
			return context.Canceled
		}
	}
}

func (k *typedKeys) Read(p []byte) (int, error) {
	if err := k.wait(nil); err != nil {
		return 0, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, k.buf)
	k.buf = k.buf[n:]
	return n, nil
}

// Ready is Options.InputReady of the keyboard.
func (k *typedKeys) Ready(ctx context.Context) error {
	if k.wait(ctx.Done()) != nil {
		return ctx.Err()
	}
	return nil
}

// readyManifestEnv is the manifest step on a terminal that has a key mode and can be waited on, as the terminal of a person: a full screen,
// whose hidden reader of the caller must not be used.
func readyManifestEnv(t *testing.T) (githubEnv, *syncBuffer, *typedKeys) {
	t.Helper()
	return readyManifestEnvWith(t, liveContext(t), 100, 30)
}

// readyManifestEnvWith is readyManifestEnv on a terminal of width by height, whose screen ends with ctx: the context that Ctrl-C cancels.
func readyManifestEnvWith(t *testing.T, ctx context.Context, width, height int) (githubEnv, *syncBuffer, *typedKeys) {
	t.Helper()
	keys := newTypedKeys(t)
	out := &syncBuffer{}
	br := bufio.NewReader(keys)
	toggle := &keyToggle{}
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: ctx, Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: width, Height: height,
		KeyInput: toggle.toggle, InputReady: keys.Ready, InputIsTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	noReader := func() ([]byte, error) {
		return nil, errors.New("the hidden reader of the caller cannot be interrupted and was used")
	}
	r, err := NewResolver(map[string]string{}, map[string]string{}, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: noReader})
	if err != nil {
		t.Fatal(err)
	}
	return githubEnv{R: r, Out: scr, WebURL: "https://github.example", Tier: config.TierCoLocated, ManifestWait: 5 * time.Second, PasteAfter: 40 * time.Millisecond}, out, keys
}

func TestAnOpenPasteEndsWhenGitHubAnswersAndTheFlowGoesOn(t *testing.T) {
	env, out, keys := readyManifestEnv(t)
	done := startManifestWith(liveContext(t), env, "acme")
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	waitForText(t, out, "No answer from GitHub yet")
	callback(t, base, "latecode1", stateOf(t, action))
	if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "latecode1" { // no key was typed
		t.Fatalf("result = %+v, want the flow to go on with the code of the callback and no key\n%s", res, out.String())
	}
	if strings.Contains(out.String(), "latecode1") {
		t.Errorf("the code of the callback was drawn:\n%s", out.String())
	}

	// The next question has the keyboard to itself.
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
	keys.Type("\r")
	select {
	case a := <-asked:
		if a.err != nil || !slices.Equal(a.picked, []string{"acme/api", "acme/web"}) {
			t.Errorf("the multiple choice = %+v, want its defaults", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the multiple choice did not take its Enter")
	}
}

// What the paste was for still works on such a terminal: a pasted address, a refusal, and an Enter that keeps waiting.
func TestTheOpenPasteStillTakesAnAddressAndAnEnterKeepsWaiting(t *testing.T) {
	env, out, keys := readyManifestEnv(t)
	done := startManifestWith(liveContext(t), env, "acme")
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	waitForText(t, out, "No answer from GitHub yet")
	keys.Type("\r") // keeps waiting: the offer comes again
	time.Sleep(150 * time.Millisecond)
	keys.Type(base + "callback?code=urlcode1&state=other\r")
	waitForText(t, out, "That was refused")
	keys.Type(base + "callback?code=urlcode1&state=" + stateOf(t, action) + "\r")
	if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "urlcode1" {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(out.String(), "urlcode1") {
		t.Errorf("the code of the pasted address was drawn:\n%s", out.String())
	}
}

// The question says what the person can do when GitHub has not answered: where the callback ends the paste, Enter is no more than the wish to
// keep waiting, and a browser that shows an error is what the paste is for.
func TestTheQuestionOfAPasteThatTheCallbackEndsDoesNotAskForAnEnterToContinue(t *testing.T) {
	env, out, _ := readyManifestEnv(t)
	done := startManifestWith(liveContext(t), env, "acme")
	waitForListener(t, out)
	waitForText(t, out, "No answer from GitHub yet")
	frame := lastFrame(out.String())
	for _, want := range []string{"If the browser shows an error, paste its address, or press Enter to keep waiting (input is hidden): "} {
		if !shows(frame, want) {
			t.Errorf("the question lacks %q:\n%s", want, frame)
		}
	}
	if shows(frame, "SuiteWard received the answer") || strings.Contains(flat(frame), ") (") {
		t.Errorf("the question still asks for an Enter when the browser says it received the answer, or has two parentheses in a row:\n%s", frame)
	}
	_ = done
}

// The paste is still a read that the run can end, on a terminal that can be waited on: the wait for GitHub, Ctrl-C and a pasted address
// end it, and the console is put back each time.
func TestTheOpenPasteIsAReadThatTheRunCanEndOnATerminalThatCanBeWaitedOn(t *testing.T) {
	t.Run("a pasted address", func(t *testing.T) {
		env, out, keys := readyManifestEnv(t)
		done := startManifestWith(liveContext(t), env, "acme")
		waitForListener(t, out)
		waitForText(t, out, "No answer from GitHub yet")
		keys.Type("pastedcode1\r")
		if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "pastedcode1" {
			t.Fatalf("result = %+v\n%s", res, out.String())
		}
		if strings.Contains(out.String(), "pastedcode1") {
			t.Errorf("what was pasted reached the output:\n%s", out.String())
		}
	})
	t.Run("the wait for GitHub is over", func(t *testing.T) {
		env, out, _ := readyManifestEnv(t)
		env.ManifestWait = 300 * time.Millisecond
		res := awaitFor(t, startManifestWith(liveContext(t), env, "acme"), 3*time.Second)
		if res.err == nil || !strings.Contains(res.err.Error(), "timed out") {
			t.Fatalf("result = %+v, want the wait to time out although the paste was open\n%s", res, out.String())
		}
	})
	t.Run("Ctrl-C", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		env, out, _ := readyManifestEnvWith(t, ctx, 100, 30)
		done := startManifestWith(ctx, env, "acme")
		waitForListener(t, out)
		waitForText(t, out, "No answer from GitHub yet")
		time.Sleep(50 * time.Millisecond) // the question is open
		cancel()
		if res := awaitFor(t, done, 3*time.Second); !errors.Is(res.err, context.Canceled) {
			t.Fatalf("result = %+v, want the interrupt", res)
		}
	})
}

// On a small terminal the question does not push the address, the name of the App and the command of the port forward off the screen.
func TestTheQuestionOfAPasteThatTheCallbackEndsFitsASmallScreen(t *testing.T) {
	env, out, keys := readyManifestEnvWith(t, liveContext(t), 80, 24)
	env.Tier = config.TierIsolated
	done := startManifestWith(liveContext(t), env, "acme")
	base := waitForListener(t, out)
	address := strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/")
	waitForText(t, out, "No answer from GitHub yet")
	frame := lastFrame(out.String())
	for _, want := range []string{address, "AWS-StartPortForwardingSession", "portNumber", "paste its address"} {
		if !shows(frame, want) {
			t.Errorf("the screen no longer shows %q:\n%s", want, frame)
		}
	}
	if !appNamePattern.MatchString(frame) {
		t.Errorf("the screen no longer shows the name of the App:\n%s", frame)
	}
	keys.Type("pastedcode\r")
	if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "pastedcode" {
		t.Fatalf("result = %+v", res)
	}
}
