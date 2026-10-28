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
	for _, want := range []string{"Instance ID: " + testInstance + "\n", "Version: v0.2.0\n"} {
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
	for _, want := range []string{"Instance ID: " + testInstance + "\n", "Version: v0.2.0\n", "Image: " + digestOf(testImage) + " (was "} {
		if !strings.Contains(tail, want) {
			t.Errorf("the summary lacks %q:\n%s", want, tail)
		}
	}
	if got := len(regexp.MustCompile(`(?m)^Version: `).FindAllString(tail, -1)); got != 1 {
		t.Errorf("the summary has %d Release rows, want 1:\n%s", got, tail)
	}
}

// The isolated tier has the same rows: the instance id, the version as the Release row, and, in an upgrade, the Image row.
func TestTheSummaryOfAnIsolatedUpgradeHasTheSameRows(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	fastHealthWait(t)
	args := []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatal(err)
	}
	tail := c.tail()
	for _, want := range []string{"Instance ID: " + testInstance + "\n", "Version: v0.2.0\n", "Image: " + testDigest + " (was " + oldDigest + ")\n"} {
		if !strings.Contains(tail, want) {
			t.Errorf("the summary lacks %q:\n%s", want, tail)
		}
	}
	if got := len(regexp.MustCompile(`(?m)^Version: `).FindAllString(tail, -1)); got != 1 {
		t.Errorf("the summary has %d Release rows, want 1:\n%s", got, tail)
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
		if last := asked[len(asked)-1]; last != "Release image:" {
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
	mustSay(t, v, "Type yes to accept the risks below.")
	if row := v.row("- Docker's service"); !strings.HasPrefix(row, "    - Docker") {
		t.Errorf("the first point is %q, want it under the head's text, behind its dash:\n%s", row, v.dump())
	}
	if row := v.row("whoever controls it can reach"); !strings.HasPrefix(row, "      whoever") {
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
	mustSay(t, v, "Backup tier: isolated; this instance: co-located.", "Type the instance id "+testInstance+" to confirm")
}

// ---- what stays as it was ----

// The answers of the image question are the ones it always took: Enter keeps the default, a typed reference replaces it, and a build that
// has no default asks the question it asked, with nothing to show.
func TestTheImageQuestionOfAFullScreenKeepsItsAnswers(t *testing.T) {
	other := "ghcr.io/other/suiteward@sha256:" + strings.Repeat("b", 64)
	for name, tc := range map[string]struct{ input, want string }{
		"Enter keeps the default":       {"\n", testImage},
		"a typed reference replaces it": {other + "\n", other},
	} {
		t.Run(name, func(t *testing.T) {
			scr, br, _ := fullScreenOn(t, liveContext(t), strings.NewReader(tc.input))
			r := newTestResolver(t, map[string]string{}, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: noSecret})
			if got, err := askImage(r, stackInstalled{Image: testImage}); err != nil || got != tc.want {
				t.Fatalf("askImage = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
	t.Run("a build that carries no release image", func(t *testing.T) {
		scr, br, out := fullScreenOn(t, liveContext(t), strings.NewReader(testImage+"\n"))
		r := newTestResolver(t, map[string]string{}, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: noSecret})
		if got, err := askImage(r, stackInstalled{}); err != nil || got != testImage {
			t.Fatalf("askImage = %q, %v, want the typed image", got, err)
		}
		if !shows(out.String(), "Release image (with its digest)") || shows(out.String(), "Default "+releaseImageRepo) {
			t.Errorf("a question with no default is not the one it was:\n%s", out.String())
		}
	})
}

// A repository with no check run yet has nothing to pick from: the question is the typed one, on its page, with no list.
func TestTheTestCheckOfARepositoryWithNoCheckRunIsTyped(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos[0].checkRuns = nil
	env, in := keyedFlowEnv(t, g, without(existingFlags(t, nil), "test-check"), "custom / ci\n")
	res, err := resolve(t, env)
	if err != nil || res.TestChecks["acme/app"] != "custom / ci" {
		t.Fatalf("resolveGitHub = %v, %v, want the typed name", res.TestChecks, err)
	}
	v := viewOf(t, in.shown[0], 100, 30)
	if !strings.Contains(v.text(), "Name of the check your CI publishes for acme/app") || strings.Contains(v.text(), "another name") || strings.Contains(v.hint, "↑/↓") {
		t.Errorf("the question of a repository with no check run is not the typed one:\n%s", v.dump())
	}
}

// A plain restore keeps the long warning about the tier: only a full screen, which has few rows, says it in a row.
func TestThePlainRestoreOfABackupOfAnotherTierKeepsTheLongWarning(t *testing.T) {
	u := restoreHarness(t)
	file := aBackup(t, func(b *backup, _ *backupEngine) { b.tier = config.TierIsolated })
	if err := u.restore(file, confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	want := "The backup was made on the isolated tier and this instance is on the co-located tier: the owner's enrollment records its tier, and /status says so after the restore."
	if !strings.Contains(u.out.String(), want) {
		t.Errorf("the plain output lacks the warning about the tier:\n%s", u.out.String())
	}
}

// ---- the default of the test check ----

// The check run that Enter takes is the only one with "test" in its name; with none or several, or with several runs and no such name,
// nothing is highlighted and Enter is refused until the person has picked one with the arrows.
func TestTheTestCheckDefaultIsTheOnlyRunWithTestInItsName(t *testing.T) {
	for name, tc := range map[string]struct {
		runs []string // listed in alphabetical order, capitals first
		want string
	}{
		"in the last place":       {[]string{"lint", "unit-tests", "build"}, "unit-tests"},
		"in whatever case":        {[]string{"build", "e2e-TESTS", "lint"}, "e2e-TESTS"},
		"with another and a test": {[]string{"CI / test", "lint"}, "CI / test"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.repos[0].checkRuns = tc.runs
			env, in := keyedFlowEnv(t, g, without(existingFlags(t, nil), "test-check"), enter)
			res, err := resolve(t, env)
			if err != nil || res.TestChecks["acme/app"] != tc.want {
				t.Fatalf("resolveGitHub = %v, %v, want %s (Enter takes the only run with test in its name)", res.TestChecks, err, tc.want)
			}
			if v := viewOf(t, in.shown[0], 100, 30); !strings.Contains(v.text(), "● "+tc.want+" (default)") || strings.Count(v.text(), "(default)") != 1 {
				t.Errorf("the default is not the run with test in its name:\n%s", v.dump())
			}
		})
	}
}

func TestTheTestCheckHasNoDefaultWhenNoneOrSeveralRunsHaveTestInTheirName(t *testing.T) {
	for name, tc := range map[string]struct {
		runs []string
		want string // the first run in the list, one key down from none
	}{
		"none has test in its name": {[]string{"lint", "build"}, "build"},
		"two have it":               {[]string{"unit-test", "e2e-test"}, "e2e-test"},
		"two, in different cases":   {[]string{"unit-TEST", "e2e-test", "lint"}, "e2e-test"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.repos[0].checkRuns = tc.runs
			env, in := keyedFlowEnv(t, g, without(existingFlags(t, nil), "test-check"), enter, keyDown, enter)
			res, err := resolve(t, env)
			if err != nil || res.TestChecks["acme/app"] != tc.want {
				t.Fatalf("resolveGitHub = %v, %v, want %s (the first run, picked with a key down)", res.TestChecks, err, tc.want)
			}
			if len(in.shown) < 3 {
				t.Fatalf("Enter at the start was not refused: the question was read %d times, want 3", len(in.shown))
			}
			start := viewOf(t, in.shown[0], 100, 30)
			if strings.Contains(start.text(), "●") || strings.Contains(start.text(), "(default)") {
				t.Errorf("a run is highlighted or marked as the default:\n%s", start.dump())
			}
			if refused := viewOf(t, in.shown[1], 100, 30); !strings.Contains(refused.text(), "Pick one with the arrows.") {
				t.Errorf("Enter was not refused with a note:\n%s", refused.dump())
			}
		})
	}
}

// A single check run is the default, whatever its name, as before.
func TestASingleCheckRunIsTheDefaultWhateverItsName(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos[0].checkRuns = []string{"lint"}
	env, in := keyedFlowEnv(t, g, without(existingFlags(t, nil), "test-check"), enter)
	res, err := resolve(t, env)
	if err != nil || res.TestChecks["acme/app"] != "lint" {
		t.Fatalf("resolveGitHub = %v, %v, want lint", res.TestChecks, err)
	}
	if v := viewOf(t, in.shown[0], 100, 30); !strings.Contains(v.text(), "● lint (default)") {
		t.Errorf("the single run is not the default:\n%s", v.dump())
	}
}

// ---- the purge page of an instance that nothing names ----

// An instance whose id is unknown is "this instance" in the box, not "instance unknown", and the word that is typed is still the id.
func TestThePurgeBoxOfAnInstanceWithNoIdSaysThisInstance(t *testing.T) {
	h := purgeHarness(t)
	h.status = func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
	h.f.fail("psql", errors.New("the database container is not running"))
	run := newScreenRun(termWidth, termHeight, unknownValue+"\n", "\n")
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--purge"}
	if err := Uninstall(liveContext(t), args, run.on(h.newEnv())); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 0)
	mustSay(t, v, "This deletes the database, the artifacts, the secrets and the settings of this instance.", "Type the instance id "+unknownValue+" to confirm")
	if strings.Contains(v.text(), "of instance "+unknownValue) {
		t.Errorf("the box calls the instance %q:\n%s", unknownValue, v.dump())
	}
}
