package installer

import (
	"context"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// SuiteWard publishes `SuiteWard / Contract` itself: it is never the project's test check, whichever way setup is reached.
func TestSetupRefusesTheContractCheckAsATestCheck(t *testing.T) {
	setupRefusesItsOwnCheck(t, protection.ContractCheck)
}

// `SuiteWard / Receipt` is SuiteWard's own as well, and typed by hand it is refused like the contract check.
func TestSetupRefusesTheReceiptCheckAsATestCheck(t *testing.T) {
	setupRefusesItsOwnCheck(t, github.ReceiptCheckName)
}

func setupRefusesItsOwnCheck(t *testing.T, contract string) {
	t.Helper()
	t.Run("enroll, from the flag", func(t *testing.T) {
		h := enrollHarness(t, statusNoOwner)
		code, stderr := h.enroll("", "--owner", "alice", "--test-check", "Acme/api="+contract)
		if code != 1 || !strings.Contains(stderr, contract) || len(h.f.tty) != 0 {
			t.Errorf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
		}
	})
	t.Run("enroll, from the settings", func(t *testing.T) {
		h := enrollHarnessWith(t, statusNoOwner, func(s *Settings) { s.GitHub.TestChecks["Acme/web"] = contract })
		code, stderr := h.enroll("", "--owner", "alice")
		if code != 1 || !strings.Contains(stderr, contract) || len(h.f.tty) != 0 {
			t.Errorf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
		}
	})
	t.Run("enroll, from the prompt", func(t *testing.T) {
		h := enrollHarnessWith(t, statusNoOwner, func(s *Settings) { s.GitHub.TestChecks = nil })
		code, stderr := h.enroll("CI / test\n"+contract+"\n", "--owner", "alice")
		if code != 1 || !strings.Contains(stderr, contract) || len(h.f.tty) != 0 {
			t.Errorf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
		}
	})
	t.Run("install", func(t *testing.T) {
		withTerminal(t, true)
		h := newHarness(t, "linux")
		h.github = func(context.Context, githubEnv) (GitHubResult, error) {
			res := githubResult()
			res.TestChecks["Acme/web"] = contract
			return res, nil
		}
		err := h.podman(githubFlags...)
		if err == nil || !strings.Contains(err.Error(), contract) || len(h.f.tty) != 0 {
			t.Errorf("got %v, setup %v", err, h.f.tty)
		}
	})
}

// Enroll refuses the co-located tier when there is no settings file, so the message does not offer it as a flag of enroll: it names the
// isolated tier and the `install --upgrade --tier co-located` that makes the file (on Linux without settings, `install --upgrade` alone asks
// for the tier too).
func TestEnrollOnLinuxWithoutSettingsNeedsTheTier(t *testing.T) {
	withTerminal(t, true)
	useDefaultConfigDir(t)
	h := newHarness(t, "linux") // no settings file: the tier would be a guess
	h.status = statusIs(statusNoOwner)
	term, _ := fakeTerminal(true, "")
	code, stderr := h.enrollWith(term, "--owner", "alice")
	if code != 1 || !strings.Contains(stderr, "pass --tier isolated") || !strings.Contains(stderr, "`suiteward install --upgrade --tier co-located`") || strings.Contains(stderr, "or --tier co-located") || len(h.f.tty) != 0 {
		t.Fatalf("exit %d, stderr %q, setup %v", code, stderr, h.f.tty)
	}
}

func TestEnrollCommandNamesTheSettingsFlagOnlyAndQuotesIt(t *testing.T) {
	term := Terminal{In: strings.NewReader(""), Out: &strings.Builder{}}
	for name, c := range map[string]struct {
		flags, answers map[string]string
		isolated       bool
		want           string
	}{
		"none":               {nil, nil, false, "suiteward enroll"},
		"isolated":           {nil, nil, true, "suiteward enroll --tier isolated"},
		"a plain path":       {map[string]string{"settings": "/home/me/s.yaml"}, nil, false, "suiteward enroll --settings /home/me/s.yaml"},
		"a path with spaces": {map[string]string{"settings": "C:/Users/me/My Settings/s.yaml"}, nil, true, "suiteward enroll --tier isolated --settings 'C:/Users/me/My Settings/s.yaml'"},
		"the answers file":   {nil, map[string]string{"settings": "/elsewhere/s.yaml"}, false, "suiteward enroll"}, // the command line of the run is the flags'
	} {
		r := newTestResolver(t, c.flags, c.answers, term)
		if got := enrollCommand("suiteward", r, c.isolated); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestEnrollNamesTheOwnerItReplaces(t *testing.T) {
	h := enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("enroll\n", "--owner", "alice"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(h.out.String(), "replaces the owner bob with alice") {
		t.Errorf("the warning does not say whom it replaces:\n%s", h.out.String())
	}
	h = enrollHarness(t, statusEnrolled)
	if code, stderr := h.enroll("enroll\n", "--owner", "BOB"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(h.out.String(), "replaces the owner") || !strings.Contains(h.out.String(), "replaces the TOTP secret") {
		t.Errorf("the same owner, in another letter case, was warned about a replacement of the owner:\n%s", h.out.String())
	}
}
