package installer

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// fullManifestEnv is the environment of the manifest step on a full screen with a context that can be cancelled, as a command has it:
// the paste is read by a goroutine of the flow, through the Screen, while the callback listener waits.
func fullManifestEnv(t *testing.T, in io.Reader) (githubEnv, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	br := bufio.NewReader(in)
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: liveContext(t), Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	r, err := NewResolver(map[string]string{}, map[string]string{}, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no password here") }})
	if err != nil {
		t.Fatal(err)
	}
	return githubEnv{R: r, Out: scr, WebURL: "https://github.example", Tier: config.TierCoLocated, ManifestWait: 5 * time.Second}, out
}

func TestManifestCodeCanBePastedOnAFullScreen(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	env, out := fullManifestEnv(t, pr)
	done := startManifest(env, "acme")
	waitForListener(t, out)
	_, _ = io.WriteString(pw, "pastedcode\n")
	if res := await(t, done); res.err != nil || res.code != "pastedcode" {
		t.Fatalf("result = %+v", res)
	}
}

// The callback comes first: the paste read is still pending, and the user's Enter ends it before the flow goes on.
func TestManifestCallbackFirstOnAFullScreenWaitsForTheEnter(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	env, out := fullManifestEnv(t, pr)
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	callback(t, base, "browsercode", stateOf(t, action))
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Press Enter") {
		if time.Now().After(deadline) {
			t.Fatalf("the user was not told to press Enter:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case res := <-done:
		t.Fatalf("returned while the paste read was still pending: %+v", res)
	default:
	}
	_, _ = io.WriteString(pw, "\n")
	if res := await(t, done); res.err != nil || res.code != "browsercode" {
		t.Fatalf("result = %+v", res)
	}
}
