package installer

import (
	"strings"
	"testing"
)

// The install explains itself on a full screen: every question has a page of its own that says what it is, the choices with their
// trade-off, and the default. A plain run, a script and an agent keep the questions they always had (explain_plain_test.go).

var coLocatedSteps = []string{"Environment", "Checks", "GitHub", "Start", "Owner", "Done"}

// installArgs name everything of a co-located Podman install but the questions of a test: the ones it leaves out are asked.
func installArgs(h *harness, flags ...string) []string {
	return h.screenArgs(flags...)
}

// askedQuestions is the install on a full screen with every question of the Environment step asked, and defaults for all: the tier, the
// runtime, the two ports and the release image, then the summary.
func askedQuestions(t *testing.T, w, h int) *screenRun {
	t.Helper()
	was := ImageDigest
	ImageDigest = testDigest // this binary carries a release, so the image has a default
	t.Cleanup(func() { ImageDigest = was })
	hn := newHarness(t, "linux")
	run := newScreenRun(w, h, "\n", "\n", "\n", "\n", "\n", "\n")
	if err := Install(liveContext(t), installArgs(hn, "--github", "no"), run.on(hn.newEnv())); err != nil {
		t.Fatalf("Install: %v\n%s", err, run.output())
	}
	if got := len(run.in.shown); got != 6 {
		t.Fatalf("the install asked %d questions, want 6 (tier, runtime, two ports, image, and the summary's Enter)", got)
	}
	return run
}

func TestTheTierQuestionHasAPageThatExplainsBothTiers(t *testing.T) {
	run := askedQuestions(t, termWidth, termHeight)
	v := run.question(t, 0)
	if !strings.HasPrefix(v.rows[0], "Where SuiteWard runs") {
		t.Errorf("the page of the tier question is titled %q, want \"Where SuiteWard runs\"", v.rows[0])
	}
	mustSay(t, v, "simplest way to start", "next to your coding agent", "catch mistakes, not a determined agent",
		"separate Linux server or user account that your coding agent cannot reach", "strongest protection", "To start, choose co-located", "Installation type")
	if !strings.Contains(v.hint, "Enter takes the default") {
		t.Errorf("the hint is %q, want it to say that Enter takes the default", v.hint)
	}
}

// The step column is empty at the first question of the install today: the person cannot see what is coming.
func TestTheStepsAreListedFromTheTierQuestionOn(t *testing.T) {
	run := askedQuestions(t, termWidth, termHeight)
	v := run.question(t, 0)
	for _, name := range coLocatedSteps {
		if !strings.Contains(v.steps, name) {
			t.Errorf("the step column at the tier question lacks %q:\n%s", name, v.steps)
		}
	}
	run.console.steps(t, coLocatedSteps...) // the list the flow sets afterwards still counts step 1 of 6 to 6 of 6
}

func TestTheRuntimeQuestionHasAPageAndAChoice(t *testing.T) {
	run := askedQuestions(t, termWidth, termHeight)
	v := run.question(t, 1)
	if !strings.HasPrefix(v.rows[0], "Container program") {
		t.Errorf("the page of the runtime question is titled %q, want \"Container program\"", v.rows[0])
	}
	mustSay(t, v, "without administrator rights", "runs as root", "secrets", "can read them", "type yes")
	for _, want := range []string{"podman", "recommended", "docker", "not recommended"} {
		if !strings.Contains(v.text(), want) {
			t.Errorf("the choice lacks %q:\n%s", want, v.dump())
		}
	}
}

func TestDockerKeepsItsWarningAndTheTypedYesOnAPageOfItsOwn(t *testing.T) {
	h := dockerHarness(t, "linux")
	run := newScreenRun(termWidth, termHeight, "2\n", "yes\n", "\n") // docker, the typed yes, the summary
	args := installArgs(h, "--image", testImage, "--http-port", "27081", "--mcp-port", "27091", "--github", "no", "--tier", "co-located")
	if err := Install(liveContext(t), args, run.on(h.newEnv())); err != nil {
		t.Fatalf("Install: %v\n%s", err, run.output())
	}
	v := run.question(t, 1)
	if !strings.HasPrefix(v.rows[0], "Docker is not recommended") {
		t.Errorf("the page of the Docker confirmation is titled %q", v.rows[0])
	}
	mustSay(t, v, "Type yes to accept the risks below", "service runs as root", "in volumes", "Type yes to use Docker anyway")
	if !strings.Contains(run.output(), "Using Docker") {
		t.Errorf("the install did not go on with Docker after the typed yes:\n%s", run.output())
	}

	// A refusal stops before anything changes.
	h = dockerHarness(t, "linux")
	run = newScreenRun(termWidth, termHeight, "2\n", "no\n")
	err := Install(liveContext(t), installArgs(h, "--image", testImage, "--tier", "co-located"), run.on(h.newEnv()))
	if err == nil || !strings.Contains(err.Error(), "you did not confirm Docker") {
		t.Fatalf("Install = %v, want the refusal", err)
	}
	h.mustNotMutate()
}

// The typed yes must be an informed one at the smallest window too: when the page does not fit, the explanation goes and the warning stays,
// with the two risks in it, above the prompt.
func TestTheDockerWarningIsStillThereAtTheSmallestWindow(t *testing.T) {
	h := dockerHarness(t, "linux")
	run := newScreenRun(60, 20, "2\n", "yes\n", "\n")
	args := installArgs(h, "--image", testImage, "--http-port", "27081", "--mcp-port", "27091", "--github", "no", "--tier", "co-located")
	if err := Install(liveContext(t), args, run.on(h.newEnv())); err != nil {
		t.Fatalf("Install: %v\n%s", err, run.output())
	}
	v := run.question(t, 1)
	if !strings.HasPrefix(v.rows[0], "Docker is not recommended") {
		t.Errorf("the page of the Docker confirmation is titled %q", v.rows[0])
	}
	mustSay(t, v, "service runs as root", "in volumes", "Type yes to use Docker anyway")
	if at, prompt := strings.Index(v.text(), "service runs as root"), strings.Index(v.text(), "Type yes to use Docker anyway"); at < 0 || at > prompt {
		t.Errorf("the risks are not above the prompt:\n%s", v.dump())
	}
}

func TestThePortsAreExplainedOnOnePageBeforeTheTwoQuestions(t *testing.T) {
	run := askedQuestions(t, termWidth, termHeight)
	v := run.question(t, 2)
	if !strings.HasPrefix(v.rows[0], "Ports") {
		t.Errorf("the page of the ports is titled %q, want \"Ports\"", v.rows[0])
	}
	mustSay(t, v, "only on this computer (127.0.0.1)", "status", "change report page", "MCP", "Your coding agent connects here", "access token", "27081", "27091",
		"Change a port only if another program on this computer uses it", "nothing outside it can reach them", "HTTP port on 127.0.0.1")
	if second := run.question(t, 3); !strings.Contains(second.text(), "Ports") || !strings.Contains(second.text(), "MCP port on 127.0.0.1") {
		t.Errorf("the second port question is not on the page of the ports:\n%s", second.dump())
	}
}

func TestTheReleaseImageIsExplainedAndEnterKeepsTheOneTheBinaryCarries(t *testing.T) {
	run := askedQuestions(t, termWidth, termHeight)
	v := run.question(t, 4)
	if !strings.HasPrefix(v.rows[0], "Release image") {
		t.Errorf("the page of the image is titled %q, want \"Release image\"", v.rows[0])
	}
	mustSay(t, v, "named by its fingerprint (digest)", "Enter keeps the release this program carries", "Release image:")
}

func TestTheImageOfAnInstalledStackIsTheOneEnterKeeps(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	run := newScreenRun(termWidth, termHeight, "\n", "\n", "\n", "\n", "\n")
	env := run.on(u.env())
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--tier", "co-located", "--github", "no"}
	_ = Install(liveContext(t), args, env) // the stack is left as it was: only the question matters here
	if len(run.in.shown) == 0 {
		t.Fatalf("no question was asked:\n%s", run.output())
	}
	var image frameView
	for i := range run.in.shown {
		if v := run.question(t, i); strings.Contains(v.text(), "Release image:") {
			image = v
			break
		}
	}
	mustSay(t, image, "Enter keeps the image that is installed")
}

// What a page keeps when the window is the default 80x24: every page of the Environment step fits whole, behind no "enlarge the window" row.
func TestEveryPageOfTheEnvironmentStepFitsTheDefaultWindow(t *testing.T) {
	run := askedQuestions(t, 80, 24)
	for i, name := range []string{"tier", "runtime", "first port", "second port", "image"} {
		v := run.question(t, i)
		if strings.Contains(v.text(), trimmed) {
			t.Errorf("the page of the %s question is trimmed at 80x24:\n%s", name, v.dump())
		}
		if v.rows[0] == "" {
			t.Errorf("the page of the %s question has no title", name)
		}
	}
}

// No page carries a secret: the token copy and the key are never drawn.
func TestNoPageOfTheInstallShowsASecret(t *testing.T) {
	h := newHarness(t, "linux")
	run := newScreenRun(termWidth, termHeight, "\n", "\n", "\n", "\n", "\n", "\n")
	if err := Install(liveContext(t), installArgs(h, "--image", testImage, "--github", "no"), run.on(h.newEnv())); err != nil {
		t.Fatalf("Install: %v", err)
	}
	token := strings.TrimSpace(readFile(t, h.tokenPath()))
	if token == "" || strings.Contains(run.output(), token) {
		t.Errorf("the MCP token (%d characters) is on a page, or there is none to look for", len(token))
	}
	h.f.assertNoSecret(t, testPEM)
}
