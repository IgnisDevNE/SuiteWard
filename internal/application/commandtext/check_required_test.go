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
	{commandtext.CheckFailure, "canonical-failing", "Canonical tests failing", "fails, is missing or is flaky", "failure"},
}

// ran is the counts of a complete canonical run: 14 tests ran, and 3 are excluded.
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
		for _, w := range []string{tc.phrase, "- State: `" + tc.name + "`", "- Proposal revision: P3-R2", "- Canonical version: V4", "- Installation tier: `isolated`", requiredClosing} {
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
	_, unavailable := checkText(t, requiredCheck("canonical-unavailable"))
	if !strings.Contains(unavailable, "```\n/suiteward unblock <code>\n```") {
		t.Errorf("the unavailable summary does not offer the unblock:\n%s", unavailable)
	}
	for _, reason := range []string{"canonical-pending", "canonical-failing"} {
		if _, summary := checkText(t, requiredCheck(reason)); strings.Contains(summary, "unblock") {
			t.Errorf("%s: the summary offers an unblock, which lifts only an unavailable run:\n%s", reason, summary)
		}
	}
}

func TestRequiredCheckSummaryStatesTheCountsOfACompleteRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		state  commandtext.CheckState
	}{
		{"failing", "canonical-failing", commandtext.CheckFailure},
		{"ready", "ready", commandtext.CheckSuccess},
		{"unchanged", "unchanged", commandtext.CheckSuccess},
	} {
		c := check(tc.state, tc.reason)
		c.Execution = ran()
		title, summary := checkText(t, c)
		for _, w := range []string{"14 tests ran: 9 passed, 2 skipped, 1 failed, 1 missing, 1 flaky.", "Excluded tests (not run): 3. Excluded tests are not covered.", requiredClosing} {
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
	variants := []commandtext.Check{requiredCheck("canonical-pending"), requiredCheck("canonical-unavailable"), requiredCheck("canonical-failing")}
	ready := check(commandtext.CheckSuccess, "ready")
	ready.Execution = ran()
	unblocked := check(commandtext.CheckSuccess, "unchanged")
	unblocked.Unblocked = true
	variants = append(variants, ready, unblocked)
	for _, c := range variants {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c.Tier = tier
			title, summary := checkText(t, c)
			if tier == commandtext.CoLocated && (!strings.Contains(summary, "`co-located`") || !strings.Contains(summary, "assurance is reduced")) {
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
		"counts on a pending run":                    with(requiredCheck("canonical-pending"), counts),
		"counts on an unavailable run":               with(requiredCheck("canonical-unavailable"), counts),
		"a failing run without counts":               with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.Execution = nil }),
		"a failing run that blocks nothing":          with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 2, Passed: 2} }),
		"counts on an approval that is missing":      with(check(commandtext.CheckInProgress, "approval-missing"), counts),
		"counts on an invalid source":                with(check(commandtext.CheckFailure, "invalid-source"), counts),
		"an unblock on an approval that is missing":  with(check(commandtext.CheckInProgress, "approval-missing"), unblock),
		"an unblock on a pending run":                with(requiredCheck("canonical-pending"), unblock),
		"an unblock on a failing run":                with(requiredCheck("canonical-failing"), unblock),
		"an unblock with counts":                     with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { counts(c); unblock(c) }),
		"an unblock on a runner block":               with(check(commandtext.CheckInProgress, "runner-uncovered"), unblock),
		"a pending run that is a failure":            with(requiredCheck("canonical-pending"), func(c *commandtext.Check) { c.State = commandtext.CheckFailure }),
		"a failing run that is in progress":          with(requiredCheck("canonical-failing"), func(c *commandtext.Check) { c.State = commandtext.CheckInProgress }),
		"a negative count":                           with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 1, Passed: 2, Failed: -1} }),
		"tests that are not the sum of their states": with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 5, Passed: 2} }),
		"a negative number of excluded tests":        with(check(commandtext.CheckSuccess, "ready"), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Excluded: -1} }),
	} {
		t.Run(name, func(t *testing.T) { mustPanic(t, func() { commandtext.CheckSummary(c) }) })
	}
}
