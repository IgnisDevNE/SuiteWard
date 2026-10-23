package installer

import (
	"errors"
	"strings"
	"testing"
)

// An upgrade that started a service that was down has changed that much, whatever stops it next: what it says of what it changed says so.

func startedAndNothingElse(t *testing.T, err error, cause error) {
	t.Helper()
	if err == nil || !errors.Is(err, cause) {
		t.Fatalf("got %v, want the failure of the step", err)
	}
	for _, want := range []string{"started", "nothing else was changed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "so nothing was changed") || strings.Contains(err.Error(), "(nothing was changed") {
		t.Errorf("the error says nothing was changed, although the service was started: %v", err)
	}
}

func TestUpgradeColocatedThatFailsAfterStartingTheServiceSaysSo(t *testing.T) {
	t.Run("the backup fails", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		u.f.rules = append([]rule{{match: "pg_dump", err: errInjected}}, u.f.rules...)
		startedAndNothingElse(t, u.upgradeAt(true, "\n", "--yes"), errInjected)
		if u.startCalls(ContainerServe) != 1 {
			t.Error("the service was not started by this run, so the test proves nothing")
		}
	})
	t.Run("the new image cannot be pulled", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		u.f.rules = append([]rule{{match: "pull " + testImage, err: errInjected}}, u.f.rules...)
		err := u.upgradeAt(true, "\n", "--yes")
		startedAndNothingElse(t, err, errInjected)
		if !strings.Contains(err.Error(), "the backup is in") {
			t.Errorf("the error does not name the backup: %v", err)
		}
	})
}

func TestUpgradeIsolatedThatFailsAfterStartingTheServiceSaysSo(t *testing.T) {
	t.Run("the backup fails", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		u.f.serviceRunning = false
		u.f.failOn["pg_dump"] = errInjected
		startedAndNothingElse(t, u.upgradeAt(true, "\n", u.downUntilStarted(), "--yes"), errInjected)
		if !u.f.started {
			t.Error("the service was not started by this run, so the test proves nothing")
		}
	})
	t.Run("the new image cannot be pulled", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		u.f.serviceRunning = false
		u.f.noImage = true
		u.f.failOn["podman pull "+releaseImageRepo] = errInjected
		err := u.upgradeAt(true, "\n", u.downUntilStarted(), "--yes")
		startedAndNothingElse(t, err, errInjected)
		if !strings.Contains(err.Error(), "the backup is in") {
			t.Errorf("the error does not name the backup: %v", err)
		}
	})
}

// printable is for one line: the separators of a line and of a paragraph are line breaks too.
func TestPrintableMakesTextFromOutsideSafeOnOneLine(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"an escape sequence":             {"a\x1b[2Jb", "a[2Jb"},
		"a line break and a tab":         {"a\nb\tc", "a b c"},
		"a carriage return":              {"a\r\nb", "a b"},
		"the line and paragraph breaks":  {"a\u2028b\u2029c", "a b c"},
		"a bidirectional override":       {"a\u202eb", "ab"},
		"text that is not ASCII is kept": {"café ✓", "café ✓"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := printable(tc.in); got != tc.want {
				t.Errorf("printable(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
