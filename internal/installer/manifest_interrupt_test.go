package installer

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The paste of the manifest code is a read that the run can end: Ctrl-C and the end of the wait for GitHub stop it, and the page and the
// question about it fit a small terminal.

// keyToggle is the per-key mode of a test console: it counts how often the console was put in key mode and back.
type keyToggle struct{ enabled, restored atomic.Int32 }

func (k *keyToggle) toggle() (func() error, error) {
	k.enabled.Add(1)
	return func() error { k.restored.Add(1); return nil }, nil
}

// balanced says that the console was put in key mode, and put back as many times.
func (k *keyToggle) balanced(t *testing.T) {
	t.Helper()
	if k.enabled.Load() == 0 || k.enabled.Load() != k.restored.Load() {
		t.Errorf("the console was put in key mode %d times and back %d times, want the same number, and not none", k.enabled.Load(), k.restored.Load())
	}
}

// keyedManifestEnv is the manifest step on a terminal whose console has a key mode, and whose own hidden reader must not be used: a
// read of it is a read that nothing can end. The screen is full or plain; ctx is the context a command has, the one Ctrl-C cancels.
func keyedManifestEnv(t *testing.T, ctx context.Context, in io.Reader, plain bool) (githubEnv, *syncBuffer, *keyToggle) {
	t.Helper()
	out := &syncBuffer{}
	br := bufio.NewReader(in)
	keys := &keyToggle{}
	mode := ui.ModeFull
	if plain {
		mode = ui.ModePlain
	}
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: ctx, Version: "v0", Mode: mode, Color: ui.ColorNone, Width: 100, Height: 30, KeyInput: keys.toggle})
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

func startManifestWith(ctx context.Context, env githubEnv, account string) <-chan codeResult {
	done := make(chan codeResult, 1)
	go func() {
		code, name, err := obtainManifestCode(ctx, env, account)
		done <- codeResult{code, name, err}
	}()
	return done
}

// blockedInput is a terminal where nobody types.
func blockedInput(t *testing.T) io.Reader {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	return pr
}

func TestManifestPasteIsAReadThatTheRunCanEnd(t *testing.T) {
	for _, plain := range []bool{false, true} {
		name := "full screen"
		if plain {
			name = "plain terminal"
		}
		t.Run(name+"/a pasted address", func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			env, out, keys := keyedManifestEnv(t, liveContext(t), pr, plain)
			done := startManifestWith(liveContext(t), env, "acme")
			waitForListener(t, out)
			waitForText(t, out, "No answer from GitHub yet")
			go func() { _, _ = io.WriteString(pw, "pasted-code\r") }()
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "pasted-code" {
				t.Fatalf("result = %+v\n%s", res, out.String())
			}
			if strings.Contains(out.String(), "pasted-code") {
				t.Errorf("what was pasted reached the output:\n%s", out.String())
			}
			keys.balanced(t)
		})
		t.Run(name+"/the wait for GitHub is over", func(t *testing.T) {
			env, out, keys := keyedManifestEnv(t, liveContext(t), blockedInput(t), plain)
			env.ManifestWait = 300 * time.Millisecond
			res := awaitFor(t, startManifestWith(liveContext(t), env, "acme"), 3*time.Second)
			if res.err == nil || !strings.Contains(res.err.Error(), "timed out") {
				t.Fatalf("result = %+v, want the wait to time out although the paste was open\n%s", res, out.String())
			}
			keys.balanced(t)
		})
		t.Run(name+"/Ctrl-C", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			env, out, keys := keyedManifestEnv(t, ctx, blockedInput(t), plain)
			done := startManifestWith(ctx, env, "acme")
			waitForListener(t, out)
			waitForText(t, out, "No answer from GitHub yet")
			time.Sleep(50 * time.Millisecond) // the question is open
			cancel()
			if res := awaitFor(t, done, 3*time.Second); !errors.Is(res.err, context.Canceled) {
				t.Fatalf("result = %+v, want the interrupt", res)
			}
			keys.balanced(t)
		})
	}
}

func TestManifestPageSaysWhenTheInstallerOffersThePaste(t *testing.T) {
	env, out := fullManifestEnvWith(t, strings.NewReader(""), func() ([]byte, error) { return nil, errors.New("no password here") })
	env.PasteAfter, env.ManifestWait = 2*time.Minute, 200*time.Millisecond
	done := startManifest(env, "acme")
	waitForListener(t, out)
	frame := lastFrame(out.String())
	for _, want := range []string{"shows an error after GitHub", "offers to paste", "2 minutes"} {
		if !shows(frame, want) {
			t.Errorf("the page does not say %q:\n%s", want, frame)
		}
	}
	await(t, done)
}

// The address, the name of the App and the command of the port forward are what the user acts on: the question about the paste, asked
// again after each refusal, must not push them off a terminal of 80 by 24, where the page is only 18 rows.
func TestManifestQuestionDoesNotPushTheAddressAndTheCommandOffASmallScreen(t *testing.T) {
	hidden := newHiddenReader(t)
	env, out := fullManifestEnvSized(t, strings.NewReader(""), hidden.read, 80, 24)
	env.Tier = config.TierIsolated
	env.PasteAfter = 40 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	address := strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/")
	assertOnScreen := func(when string) {
		t.Helper()
		frame := lastFrame(out.String())
		for _, want := range []string{address, "AWS-StartPortForwardingSession", "portNumber"} {
			if !shows(frame, want) {
				t.Errorf("%s: the screen no longer shows %q:\n%s", when, want, frame)
			}
		}
		if !appNamePattern.MatchString(frame) {
			t.Errorf("%s: the screen no longer shows the name of the App:\n%s", when, frame)
		}
	}
	hidden.readsReach(t, 1, out)
	assertOnScreen("the paste is offered")
	if !shows(lastFrame(out.String()), "SuiteWard received the answer") {
		t.Errorf("the question does not say that Enter also takes the answer that came:\n%s", lastFrame(out.String()))
	}
	for i := int32(2); i <= 4; i++ {
		hidden.send("not a code!")
		hidden.readsReach(t, i, out)
		assertOnScreen("after a refusal")
		if !shows(lastFrame(out.String()), "That was refused") {
			t.Errorf("the refusal is not on the screen:\n%s", lastFrame(out.String()))
		}
	}
	hidden.send("pastedcode")
	if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "pastedcode" {
		t.Fatalf("result = %+v", res)
	}
}
