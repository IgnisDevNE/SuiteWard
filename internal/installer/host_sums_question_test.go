package installer

import (
	"errors"
	"strings"
	"testing"
)

// The host step needs the release's SHA256SUMS, a file the person brings. With a terminal it asks for it; with none it refuses, as it did, naming the
// flag (ErrUsage, exit 2).

func TestPrepareHostAsksForTheSHA256SUMSOnATerminal(t *testing.T) {
	h := newHostFake()
	flags := withReleaseSums(t, h, declineMetadataRule) // makes the file the program is listed in
	delete(flags, "sha256sums")
	out, err := runHostAsGiven(t, h, flags, true, execSums+"\n")
	if err != nil {
		t.Fatalf("a terminal that is asked for the file: %v\n%s", err, out)
	}
	if want := "Where is the release's SHA256SUMS file?"; !strings.Contains(out, want) {
		t.Errorf("the output lacks the question %q:\n%s", want, out)
	}
	if got := writeOf(h, execPinFile); !strings.HasSuffix(strings.TrimSpace(got), "suiteward_v0.4.0_linux_amd64") {
		t.Errorf("the pin is %q, want the line of the file that was typed", got)
	}
}

func TestPrepareHostDoesNotAskWhenTheFlagIsGivenOrNobodyCanBeAsked(t *testing.T) {
	t.Run("the flag answers", func(t *testing.T) {
		h := newHostFake()
		out, err := runHost(t, h, declineMetadataRule, true, "")
		if err != nil || strings.Contains(out, "Where is the release's SHA256SUMS file?") {
			t.Errorf("err %v, output:\n%s", err, out)
		}
	})
	t.Run("no terminal", func(t *testing.T) {
		h := newHostFake()
		flags := withReleaseSums(t, h, declineMetadataRule)
		delete(flags, "sha256sums")
		out, err := runHostAsGiven(t, h, flags, false, execSums+"\n")
		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "--sha256sums") || strings.Contains(out, "Where is the release's SHA256SUMS file?") {
			t.Errorf("err %v, output %q: want the usage refusal that names the flag, and no question", err, out)
		}
	})
}

// A terminal that is asked and answers nothing changes nothing.
func TestPrepareHostWithNoAnswerForTheSHA256SUMSChangesNothing(t *testing.T) {
	h := newHostFake()
	flags := withReleaseSums(t, h, declineMetadataRule)
	delete(flags, "sha256sums")
	if _, err := runHostAsGiven(t, h, flags, true, "\n"); err == nil {
		t.Fatal("an empty answer was taken for the file")
	}
	for _, c := range h.calls {
		if c.name != "id" {
			t.Errorf("a command ran before the refusal: %v", h.lines())
		}
	}
}
