package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The explanations are for the full screen. A plain run (--plain, a pipe, NO_COLOR, a small window), which is what a script and an agent
// get, keeps every question byte for byte, so that docs/agent-install.md and the scripts that answer them stay true.

func TestThePlainQuestionsOfTheInstallAreWhatTheyAlwaysWere(t *testing.T) {
	was := ImageDigest
	ImageDigest = testDigest
	t.Cleanup(func() { ImageDigest = was })
	h := newHarness(t, "linux")
	env := h.newEnv()
	env.Term = Terminal{In: strings.NewReader("\n\n\n\n\n"), Out: &h.out, IsTerminal: true, ReadPassword: noSecret}
	if err := Install(context.Background(), installArgs(h, "--github", "no"), env); err != nil {
		t.Fatalf("Install: %v\n%s", err, h.out.String())
	}
	out := h.out.String()
	for _, want := range []string{
		"Installation tier (co-located or isolated) [co-located]: ",
		"Container runtime (podman recommended, docker discouraged) [podman]: ",
		"HTTP port on 127.0.0.1 [27081]: ",
		"MCP port on 127.0.0.1 [27091]: ",
		"Release image (pinned by digest) [" + testImage + "]: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plain output lacks the question %q:\n%s", want, out)
		}
	}
	for _, page := range []string{"Where SuiteWard runs", "Container runtime\n", "Ports\n", "Release image\n", "reduced assurance", "Recommended"} {
		if strings.Contains(out, page) {
			t.Errorf("the plain output carries the explanation %q", page)
		}
	}
	if strings.Contains(out, esc) {
		t.Error("the plain output has an escape sequence")
	}
}

// --plain on a terminal that could draw is the same run.
func TestThePlainFlagKeepsTheQuestionsOfAFullScreenTerminalPlain(t *testing.T) {
	h := newHarness(t, "linux")
	run := newScreenRun(termWidth, termHeight, "\n\n\n\n\n")
	if err := Install(liveContext(t), installArgs(h, "--plain", "--image", testImage, "--github", "no"), run.on(h.newEnv())); err != nil {
		t.Fatalf("Install: %v", err)
	}
	out := run.output()
	if strings.Contains(out, esc) || !strings.Contains(out, "Installation tier (co-located or isolated) [co-located]: ") || strings.Contains(out, "Where SuiteWard runs") {
		t.Errorf("a plain run on a terminal drew or explained:\n%s", out)
	}
}

func TestThePlainDockerConfirmationIsTheWarningAndTheTypedYes(t *testing.T) {
	h := dockerHarness(t, "linux")
	env := h.newEnv()
	env.Term = Terminal{In: strings.NewReader("yes\n"), Out: &h.out, IsTerminal: true, ReadPassword: noSecret}
	args := installArgs(h, "--runtime", "docker", "--image", testImage, "--tier", "co-located", "--http-port", "27081", "--mcp-port", "27091", "--github", "no")
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := dockerWarning + "Type yes to use Docker anyway [no]: "; !strings.Contains(h.out.String(), want) {
		t.Errorf("the plain output lacks the warning and the question exactly as they were:\n%s", h.out.String())
	}
	if strings.Contains(h.out.String(), "Docker is not recommended") {
		t.Error("the plain output carries the page of the Docker confirmation")
	}
}

// A yes or no question is typed on a plain screen, "[y/N]" and all, and a choice on a full screen; the default is no in both.
func TestAYesNoQuestionIsTypedWhenPlainAndAChoiceOnAFullScreen(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		for input, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false} {
			var out bytes.Buffer
			r := newTestResolver(t, nil, nil, Terminal{In: strings.NewReader(input), Out: &out, IsTerminal: true, ReadPassword: noSecret})
			got, err := r.confirm("yes", "Go on?")
			if err != nil || got != want {
				t.Errorf("confirm(%q) = %v, %v; want %v", input, got, err, want)
			}
			if out.String() != "Go on? [y/N]: " {
				t.Errorf("the plain question is %q, want \"Go on? [y/N]: \"", out.String())
			}
		}
	})
	t.Run("full screen", func(t *testing.T) {
		for input, want := range map[string]bool{"1\n": true, "2\n": false, "\n": false} { // Yes, No, and Enter takes the default, which is No
			scr, br, out := fullScreenOn(t, context.Background(), strings.NewReader(input))
			r := newTestResolver(t, nil, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: noSecret})
			got, err := r.confirm("yes", "Go on?")
			if err != nil || got != want {
				t.Errorf("confirm(%q) = %v, %v; want %v", input, got, err, want)
			}
			v := viewOf(t, out.String(), termWidth, termHeight)
			for _, w := range []string{"Go on?", "Yes", "No"} {
				if !strings.Contains(v.text(), w) {
					t.Errorf("the choice lacks %q:\n%s", w, v.dump())
				}
			}
			if strings.Contains(v.text(), "[y/N]") {
				t.Errorf("a full screen asks the typed question:\n%s", v.dump())
			}
		}
	})
	t.Run("a flag still answers", func(t *testing.T) {
		scr, br, out := fullScreenOn(t, context.Background(), strings.NewReader(""))
		r := newTestResolver(t, map[string]string{"yes": "true"}, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: noSecret})
		if got, err := r.confirm("yes", "Go on?"); err != nil || !got || strings.Contains(out.String(), "Go on?") {
			t.Errorf("confirm = %v, %v; want yes without a question:\n%s", got, err, out.String())
		}
	})
}
