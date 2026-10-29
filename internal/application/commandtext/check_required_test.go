package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The summary of the Contract check under canonical: required (docs/contracts/runtime.md, "The canonical gate"): counts only, the tier, "Excluded tests are not
// covered." when some are, and the owner's unblock. A canonical-* reason, the counts or the unblock tell it from the usual summary.

const (
	requiredClosing = "This check reports on the protected contract and on the run of its approved tests."
	usualClosing    = "This check reports on the protected contract only."
	unblockedText   = "The owner unblocked this head: its canonical run was unavailable, so the approved tests did not run on it."
)

// requiredReasons are the reasons of the canonical gate, each with its state, its title and a phrase only its text holds.
var requiredReasons = []struct {
	state  commandtext.CheckState
	reason string
	title  string
	phrase string
	name   string
}{
	{commandtext.CheckInProgress, "canonical-pending", "Canonical run pending", "has not finished", "in_progress"},
	{commandtext.CheckInProgress, "canonical-unavailable", "Canonical run unavailable", "could not run the approved tests", "in_progress"},
	{commandtext.CheckInProgress, "canonical-not-responding", "Test runner not responding", "has not answered for about half a minute", "in_progress"},
	{commandtext.CheckFailure, "canonical-failing", "Canonical tests failing", "fails, is missing or is flaky", "failure"},
}

// passed is the counts of a complete canonical run that no test blocks: 12 tests ran, and 3 are excluded.
func passed() *commandtext.CheckExecution {
	return &commandtext.CheckExecution{Tests: 12, Passed: 9, Skipped: 3, Excluded: 3}
}

// ran is the counts of a complete canonical run with a test that blocks: 14 tests ran, and 3 are excluded.
func ran() *commandtext.CheckExecution {
	return &commandtext.CheckExecution{Tests: 14, Passed: 9, Skipped: 2, Failed: 1, Missing: 1, Flaky: 1, Excluded: 3}
}

func requiredCheck(reason string) commandtext.Check {
	for _, tc := range requiredReasons {
		if tc.reason == reason {
			c := check(tc.state, reason)
			if reason == "canonical-failing" {
				c.Execution = ran()
			}
			return c
		}
	}
	panic("unknown reason " + reason)
}

func TestRequiredCheckSummaryStatesTheCanonicalRunItIsWaitingForOrCouldNotMake(t *testing.T) {
	for _, tc := range requiredReasons {
		title, summary := checkText(t, requiredCheck(tc.reason))
		if title != tc.title {
			t.Errorf("%s: title %q, want %q", tc.reason, title, tc.title)
		}
		for _, w := range []string{tc.phrase, "- State: `" + tc.name + "`", "- Proposal revision: P3-R2", "- Canonical version: V4", "- Installation type: on a separate server", requiredClosing} {
			if !strings.Contains(summary, w) {
				t.Errorf("%s: summary lacks %q:\n%s", tc.reason, w, summary)
			}
		}
		if strings.Contains(summary, usualClosing) || strings.Contains(summary, "does not run tests") {
			t.Errorf("%s: the summary is the usual one:\n%s", tc.reason, summary)
		}
	}
}

func TestRequiredCheckSummaryOfAnUnavailableRunOffersTheUnblockAndOfAPendingOneDoesNot(t *testing.T) {
	for _, reason := range []string{"canonical-unavailable", "canonical-not-responding"} {
		if _, summary := checkText(t, requiredCheck(reason)); !strings.Contains(summary, "```\n/suiteward unblock <code>\n```") {
			t.Errorf("%s: the summary does not offer the unblock:\n%s", reason, summary)
		}
	}
	for _, reason := range []string{"canonical-pending", "canonical-failing"} {
		if _, summary := checkText(t, requiredCheck(reason)); strings.Contains(summary, "unblock") {
			t.Errorf("%s: the summary offers an unblock, which only canonical-unavailable and canonical-not-responding offer:\n%s", reason, summary)
		}
	}
}

func TestRequiredCheckSummaryStatesTheCountsOfACompleteRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		state  commandtext.CheckState
		counts *commandtext.CheckExecution
		ran    string
	}{
		{"failing", "canonical-failing", commandtext.CheckFailure, ran(), "14 tests ran: 9 passed, 2 skipped, 1 failed, 1 missing, 1 flaky."},
		{"ready", "ready", commandtext.CheckSuccess, passed(), "12 tests ran: 9 passed, 3 skipped, 0 failed, 0 missing, 0 flaky."},
		{"unchanged", "unchanged", commandtext.CheckSuccess, passed(), "12 tests ran: 9 passed, 3 skipped, 0 failed, 0 missing, 0 flaky."},
	} {
		c := check(tc.state, tc.reason)
		c.Execution = tc.counts
		title, summary := checkText(t, c)
		for _, w := range []string{tc.ran, "Excluded tests (not run): 3. Excluded tests are not covered.", requiredClosing} {
			if !strings.Contains(summary, w) {
				t.Errorf("%s: summary lacks %q:\n%s", tc.name, w, summary)
			}
		}
		if title == "" || strings.Contains(summary, usualClosing) {
			t.Errorf("%s: no title, or the usual closing sentence:\n%s", tc.name, summary)
		}
	}
}

func TestRequiredCheckSummaryOfARunWithNothingExcludedDoesNotSayThatExcludedTestsAreNotCovered(t *testing.T) {
	c := check(commandtext.CheckSuccess, "ready")
	c.Execution = &commandtext.CheckExecution{Tests: 3, Passed: 3}
	_, summary := checkText(t, c)
	if !strings.Contains(summary, "3 tests ran: 3 passed, 0 skipped, 0 failed, 0 missing, 0 flaky.") || !strings.Contains(summary, "Excluded tests (not run): 0.") ||
		strings.Contains(summary, "not covered") {
		t.Errorf("summary:\n%s", summary)
	}
}

// A canonical run with no declared test is complete: the summary says "0 tests ran".
func TestRequiredCheckSummaryOfACanonicalRunWithNoDeclaredTestSaysZeroTestsRan(t *testing.T) {
	c := check(commandtext.CheckSuccess, "unchanged")
	c.Execution = &commandtext.CheckExecution{}
	_, summary := checkText(t, c)
	if !strings.Contains(summary, "0 tests ran.") || strings.Contains(summary, "passed") {
		t.Errorf("summary:\n%s", summary)
	}
	c.Execution = &commandtext.CheckExecution{Tests: 1, Passed: 1}
	if _, summary := checkText(t, c); !strings.Contains(summary, "1 test ran: 1 passed") {
		t.Errorf("one test is not 1 test ran:\n%s", summary)
	}
}

func TestRequiredCheckSummaryOfAnUnblockedHeadSaysThatTheApprovedTestsDidNotRun(t *testing.T) {
	for _, reason := range []string{"ready", "unchanged"} {
		c := check(commandtext.CheckSuccess, reason)
		c.Unblocked = true
		_, summary := checkText(t, c)
		if !strings.Contains(summary, unblockedText) || !strings.Contains(summary, requiredClosing) || strings.Contains(summary, "tests ran") {
			t.Errorf("%s: summary:\n%s", reason, summary)
		}
	}
	if _, usual := checkText(t, check(commandtext.CheckSuccess, "ready")); strings.Contains(usual, "unblocked") {
		t.Errorf("a head that was not unblocked says so:\n%s", usual)
	}
}

func TestRequiredCheckSummaryStatesTheTierAndHoldsNoRepositoryText(t *testing.T) {
	claim := regexp.MustCompile(`(?i)\bcoverage\b|all tests pass|build passed|ci passed`)
	variants := []commandtext.Check{requiredCheck("canonical-pending"), requiredCheck("canonical-unavailable"), requiredCheck("canonical-not-responding"), requiredCheck("canonical-failing")}
	ready := check(commandtext.CheckSuccess, "ready")
	ready.Execution = passed()
	unblocked := check(commandtext.CheckSuccess, "unchanged")
	unblocked.Unblocked = true
	variants = append(variants, ready, unblocked)
	for _, c := range variants {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c.Tier = tier
			title, summary := checkText(t, c)
			if tier == commandtext.CoLocated && (!strings.Contains(summary, "on the same computer as your coding agent") || !strings.Contains(summary, "protection is reduced")) {
				t.Errorf("%s: the co-located summary lacks the tier or the reduced assurance:\n%s", c.Reason, summary)
			}
			for _, s := range []string{title, summary} {
				if s == "" || strings.Contains(s, "<!--") || strings.Contains(s, "@") || strings.Contains(s, "http") || strings.Contains(s, "#issuecomment") || len(s) > 4096 || claim.MatchString(s) {
					t.Errorf("%s: the output is empty or holds a marker, a mention, a link or a claim, or is long: %q", c.Reason, s)
				}
			}
		}
	}
}

// Any other reason under required is the usual summary, with the closing sentence it always had.
func TestAnyOtherReasonUnderRequiredHasTheUsualSummary(t *testing.T) {
	for _, tc := range checkReasons {
		if _, summary := checkText(t, check(tc.state, tc.reason)); !strings.Contains(summary, usualClosing) || strings.Contains(summary, requiredClosing) {
			t.Errorf("%s: summary:\n%s", tc.reason, summary)
		}
	}
}

func TestCheckSummaryRefusesRequiredFieldsThatDoNotFit(t *testing.T) {
	with := func(c commandtext.Check, edit func(*commandtext.Check)) commandtext.Check { edit(&c); return c }
	counts := func(c *commandtext.Check) { c.Execution = ran() }
	unblock := func(c *commandtext.Check) { c.Unblocked = true }
	for name, c := range map[string]commandtext.Check{
		"counts on a pending run":                      with(requiredCheck("canonical-pending"), counts),
		"counts on an unavailable run":                 with(requiredCheck("canonical-unavailable"), counts),
		"counts on a runner that does not respond":     with(requiredCheck("canonical-not-responding"), counts),
		"an unblock on a runner that does not respond": with(requiredCheck("canonical-not-responding"), unblock),
		"a runner that does not respond that failed":   with(requiredCheck("canonical-not-responding"), func(c *commandtext.Check) { c.State = commandtext.CheckFailure }),
		"a failing run without counts":                 with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.Execution = nil }),
		"a failing run that blocks nothing":            with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 2, Passed: 2} }),
		"counts on an invalid source":                  with(check(commandtext.CheckFailure, "invalid-source"), counts),
		"an unblock on an approval that is missing":    with(check(commandtext.CheckInProgress, "approval-missing"), unblock),
		"an unblock on a pending run":                  with(requiredCheck("canonical-pending"), unblock),
		"an unblock on a failing run":                  with(requiredCheck("canonical-failing"), unblock),
		"an unblock with counts":                       with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = passed(); unblock(c) }),
		"a success with a failing test":                with(check(commandtext.CheckSuccess, "ready"), counts),
		"an unblock on a runner block":                 with(check(commandtext.CheckInProgress, "runner-uncovered"), unblock),
		"a pending run that is a failure":              with(requiredCheck("canonical-pending"), func(c *commandtext.Check) { c.State = commandtext.CheckFailure }),
		"a failing run that is in progress":            with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.State = commandtext.CheckInProgress }),
		"a negative count":                             with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 1, Passed: 2, Failed: -1} }),
		"tests that are not the sum of their states":   with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 5, Passed: 2} }),
		"a negative number of excluded tests":          with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Excluded: -1} }),
	} {
		t.Run(name, func(t *testing.T) { mustPanic(t, func() { commandtext.CheckSummary(c) }) })
	}
}

// A runner that does not respond: the text says that it is a problem only if it lasts and that SuiteWard keeps trying, the next step is to check the runner, and the
// owner can let the commit pass without the approved tests. It holds no count, no test name and no path.
func TestRequiredCheckSummaryOfARunnerThatDoesNotRespondAsksToCheckTheRunnerAndOffersTheUnblock(t *testing.T) {
	_, summary := checkText(t, requiredCheck("canonical-not-responding"))
	for _, w := range []string{
		"The test runner has not answered for about half a minute, so the approved tests have not run on this commit. This is a problem only if it lasts. " +
			"SuiteWard keeps trying, and the tests start when the test runner answers. This check does not pass until they have run.",
		"Next: check that the test runner is running on the machine that runs SuiteWard. To let this commit pass without the approved tests, the owner comments:",
	} {
		if !strings.Contains(summary, w) {
			t.Errorf("the summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "tests ran") || strings.Contains(summary, "Excluded tests") {
		t.Errorf("the summary of a run that has not started states counts:\n%s", summary)
	}
}
