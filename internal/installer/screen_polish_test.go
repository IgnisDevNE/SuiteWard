package installer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The polish of the screens after the flows were rebuilt: the rows of the summary, the questions of the image and of the test check, the
// purge page, and the pages of a small window.

// ---- the summary ----

// The summary names the instance by its id, as everywhere else, and the release by the version the service reports.
func TestTheSummaryNamesTheInstanceByItsIdAndTheReleaseByItsVersion(t *testing.T) {
	h := newHarness(t, "linux")
	h.status = func(_ context.Context, address string) ([]byte, error) {
		if !strings.HasSuffix(address, "/status") { // the reads of doctor at the end of the install
			return nil, errors.New("connection refused")
		}
		return []byte(`{"version":"v0.2.0","instance":"` + testInstance + `"}`), nil
	}
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "none"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	tail := c.tail()
	for _, want := range []string{"Instance: " + testInstance + "\n", "Release: v0.2.0\n"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the summary lacks %q:\n%s", want, tail)
		}
	}
}

// An upgrade says which image replaced which in a row of its own, so that it has one Release row, the version, like an install.
func TestTheSummaryOfAnUpgradeHasOneReleaseRowAndAnImageRow(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	tail := c.tail()
	for _, want := range []string{"Instance: " + testInstance + "\n", "Release: v0.2.0\n", "Image: " + digestOf(testImage) + " (was "} {
		if !strings.Contains(tail, want) {
			t.Errorf("the summary lacks %q:\n%s", want, tail)
		}
	}
	if got := len(regexp.MustCompile(`(?m)^Release: `).FindAllString(tail, -1)); got != 1 {
		t.Errorf("the summary has %d Release rows, want 1:\n%s", got, tail)
	}
}

// engineOnly is a runtime that knows its engine and nothing else: the summary of the isolated tier asks for no more.
type engineOnly struct{ Runtime }

func (engineOnly) Engine() Engine { return Engine{Name: "Podman", Version: "5.8", Rootless: true} }

// The isolated tier passes its upgrade's row as Release: the summary shows it as the image, and the Release row is the version.
func TestTheSummaryOfTheIsolatedTierHasTheSameRows(t *testing.T) {
	env := Env{HTTPGet: func(context.Context, string) ([]byte, error) {
		return []byte(`{"version":"v0.2.0","instance":"` + testInstance + `"}`), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the closing checks are skipped: only the rows before them are looked at
	swap := digestOf(testImage) + " (was " + digestOf(testImage) + ")"
	rows := isolatedSummary(ctx, env, isolatedHost{rt: engineOnly{}, settingsPath: "settings.yaml"}, Settings{Tier: config.TierIsolated, HTTPPort: 27081}, ui.KV{Key: "Release", Value: swap})
	got := map[string][]string{}
	for _, kv := range rows {
		got[kv.Key] = append(got[kv.Key], kv.Value)
	}
	for key, want := range map[string]string{"Instance": testInstance, "Release": "v0.2.0", "Image": swap} {
		if len(got[key]) != 1 || got[key][0] != want {
			t.Errorf("the %s rows are %q, want exactly %q", key, got[key], want)
		}
	}
}

// ---- the image question ----

// The default of the question is on the page, in a row of its own, and the question is short: a long reference in the prompt wrapped
// until the cursor stood alone on a row.
func TestTheDefaultImageIsOnThePageAndTheQuestionIsShort(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{termWidth, termHeight}, {60, 20}} {
		run := askedQuestions(t, sz.w, sz.h)
		v := run.question(t, 4)
		if !strings.Contains(strings.ReplaceAll(v.text(), " ", ""), "Default"+testImage) {
			t.Errorf("%dx%d: the page has no row that gives the default image:\n%s", sz.w, sz.h, v.dump())
		}
		var asked []string
		for _, r := range v.rows {
			if strings.TrimSpace(r) != "" {
				asked = append(asked, r)
			}
		}
		if last := asked[len(asked)-1]; last != "Release image (Enter keeps the default):" {
			t.Errorf("%dx%d: the last row of the page is %q, want the short question alone on it:\n%s", sz.w, sz.h, last, v.dump())
		}
		if !strings.Contains(v.hint, "Enter takes the default") {
			t.Errorf("%dx%d: the hint is %q, want it to say that Enter takes the default", sz.w, sz.h, v.hint)
		}
	}
}

// ---- the test check ----

// framedInput is the input of a terminal that sends one chunk per read, and keeps what the screen showed while each read waited.
type framedInput struct {
	out    *syncBuffer
	chunks []string
	shown  []string
}

func (f *framedInput) Read(p []byte) (int, error) {
	f.shown = append(f.shown, f.out.String())
	if len(f.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.chunks[0])
	f.chunks = f.chunks[1:]
	return n, nil
}

// keyedFlowEnv is flowEnv on a full screen that reads keys: its chunks are the key presses and the lines, one per read.
func keyedFlowEnv(t *testing.T, g *fakeGitHub, flags map[string]string, chunks ...string) (githubEnv, *framedInput) {
	t.Helper()
	env, _ := flowEnv(t, g, flags, strings.NewReader(""), true)
	out := &syncBuffer{}
	in := &framedInput{out: out, chunks: chunks}
	br := bufio.NewReader(in)
	keys := func() (func() error, error) { return func() error { return nil }, nil }
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: liveContext(t), Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30, KeyInput: keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	r, err := NewResolver(env.R.flags, env.R.answers, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no password here") }})
	if err != nil {
		t.Fatal(err)
	}
	env.R, env.Out = r, scr
	return env, in
}

const (
	keyDown = "\x1b[B"
	enter   = "\n"
)

// The test check is picked from the check runs seen with the arrow keys, under a blank line and a rule that set the list apart from the
// paragraph above it; the last choice is to type another name.
func TestTheTestCheckIsPickedWithTheArrowKeys(t *testing.T) {
	env, in := keyedFlowEnv(t, newFakeGitHub(t), without(existingFlags(t, nil), "test-check"), keyDown, enter)
	res, err := resolve(t, env)
	if err != nil || res.TestChecks["acme/app"] != "lint" {
		t.Fatalf("resolveGitHub = %v, %v, want lint (the second check run, one key down)", res.TestChecks, err)
	}
	v := viewOf(t, in.shown[0], 100, 30)
	for _, want := range []string{"Required test check for acme/app", "● CI / test", "○ lint", "○ another name…"} {
		if !strings.Contains(v.text(), want) {
			t.Errorf("the page lacks %q:\n%s", want, v.dump())
		}
	}
	if !strings.Contains(v.hint, "↑/↓ to choose") {
		t.Errorf("the hint is %q, want the arrow keys", v.hint)
	}
	at := -1
	for i, r := range v.rows {
		if strings.Contains(r, "seen on the default branch.") {
			at = i
		}
	}
	if at < 0 || at+2 >= len(v.rows) || v.rows[at+1] != "" || !strings.HasPrefix(v.rows[at+2], "──") {
		t.Errorf("the list is not set apart from the paragraph by a blank row and a rule:\n%s", v.dump())
	}
}

// "another name…" asks the name that CI will publish, as the question did before there was a list.
func TestAnotherNameForTheTestCheckIsTyped(t *testing.T) {
	env, in := keyedFlowEnv(t, newFakeGitHub(t), without(existingFlags(t, nil), "test-check"), keyDown, keyDown, enter, "my / ci\n")
	res, err := resolve(t, env)
	if err != nil || res.TestChecks["acme/app"] != "my / ci" {
		t.Fatalf("resolveGitHub = %v, %v, want the typed name", res.TestChecks, err)
	}
	if v := viewOf(t, in.shown[3], 100, 30); !strings.Contains(v.text(), "Name of the check your CI publishes for acme/app") {
		t.Errorf("the typed question is not asked after the choice:\n%s", v.dump())
	}
}

// ---- the small window ----

// The Docker confirmation fits the smallest window whole: the sentence that says that the typed answer is a confirmation is not cut
// behind a row that says so, and the points of the warning hang under their text.
func TestTheDockerPageFitsTheSmallestWindowAndItsPointsHangUnderTheirText(t *testing.T) {
	h := dockerHarness(t, "linux")
	run := newScreenRun(60, 20, "2\n", "yes\n", "\n")
	args := installArgs(h, "--image", testImage, "--http-port", "27081", "--mcp-port", "27091", "--github", "no", "--tier", "co-located")
	if err := Install(liveContext(t), args, run.on(h.newEnv())); err != nil {
		t.Fatalf("Install: %v\n%s", err, run.output())
	}
	v := run.question(t, 1)
	if strings.Contains(v.text(), trimmed) {
		t.Errorf("the Docker page is trimmed at 60x20:\n%s", v.dump())
	}
	mustSay(t, v, "This is a confirmation: typing yes accepts the risks below.")
	if row := v.row("- Docker's daemon"); !strings.HasPrefix(row, "    - Docker") {
		t.Errorf("the first point is %q, want it under the head's text, behind its dash:\n%s", row, v.dump())
	}
	if row := v.row("its containers are not isolated"); !strings.HasPrefix(row, "      its containers") {
		t.Errorf("what the first point wraps to is %q, want it under the text after the dash:\n%s", row, v.dump())
	}
}

// A backup of the other tier adds a warning to a page that has few rows for it: the warning is short on a full screen, and the page still
// shows which instance is replaced by which backup at 80x24.
func TestTheRestorePageOfABackupOfAnotherTierStillShowsTheInstanceAndTheReleaseAt80x24(t *testing.T) {
	run, _ := restoreOnScreen(t, 80, 24, config.TierIsolated)
	v := run.question(t, 0)
	for _, fact := range []string{"  Instance", "  Release"} {
		if row := v.row(fact); !strings.HasPrefix(row, fact) {
			t.Errorf("the page does not show the fact %q of the backup (row %q):\n%s", strings.TrimSpace(fact), row, v.dump())
		}
	}
	mustSay(t, v, "The backup is from the isolated tier; this instance is on the co-located tier.", "Type the instance id "+testInstance+" to confirm")
}
