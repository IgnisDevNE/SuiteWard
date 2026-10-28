package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The summary of a head that awaits the owner's approval (docs/contracts/runtime.md, "Check reason approval-missing" and "The canonical gate"): the approve command
// names a mode when the revision comment does, and under canonical: required the summary states the counts of a complete canonical run.

const (
	modeCommand = "```\n/suiteward approve P3-R2 <fix|feature|refactor> <code>\n```"
	plainSteps  = "```\n/suiteward approve P3-R2 <code>\n```"
	blocking    = "At least one approved test fails, is missing or is flaky on this head. If this change edits that test, read the change before you approve: an edited test can pass by checking less."
)

// awaiting is the check of a head that awaits approval against V4, with the counts of a canonical run and the mode of the approve as the caller sets them.
func awaiting(counts *commandtext.CheckExecution, mode bool) commandtext.Check {
	c := check(commandtext.CheckInProgress, "approval-missing")
	c.Execution, c.ApproveMode = counts, mode
	return c
}

func TestCheckSummaryOfAMissingApprovalNamesTheModeWhenTheReceiptIsRequired(t *testing.T) {
	_, summary := checkText(t, awaiting(nil, true))
	for _, w := range []string{modeCommand, "name the mode of this change", "Replace `<fix|feature|refactor>` with the mode of this change and `<code>` with a current code from your authenticator app."} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, plainSteps) {
		t.Errorf("the summary also offers the approve that a required receipt refuses:\n%s", summary)
	}
	_, plain := checkText(t, awaiting(nil, false))
	if !strings.Contains(plain, plainSteps) || strings.Contains(plain, "<fix|feature|refactor>") || strings.Contains(plain, "mode") {
		t.Errorf("a revision that needs no mode shows one:\n%s", plain)
	}
}

// The check and the revision comment name the mode with the same command, so the owner copies one form from either.
func TestCheckSummaryAndRevisionCommentOfARequiredReceiptShowTheSameApproveCommand(t *testing.T) {
	c := awaiting(nil, true)
	c.Proposal, c.Revision = 7, 3 // the revision of receiptRevision
	_, summary := checkText(t, c)
	comment := revisionText(t, receiptRevision(t, "required"))
	command := "```\n/suiteward approve P7-R3 <fix|feature|refactor> <code>\n```"
	if !strings.Contains(summary, command) || !strings.Contains(comment, command) {
		t.Errorf("the check and the comment do not show the same command %q:\n%s\n---\n%s", command, summary, comment)
	}
}

func TestCheckSummaryOfAMissingApprovalStatesTheCountsOfACompleteCanonicalRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts *commandtext.CheckExecution
		ran    string
		block  bool
	}{
		{"nothing blocks", passed(), "12 tests ran: 9 passed, 3 skipped, 0 failed, 0 missing, 0 flaky.", false},
		{"a test blocks", ran(), "14 tests ran: 9 passed, 2 skipped, 1 failed, 1 missing, 1 flaky.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, summary := checkText(t, awaiting(tc.counts, false))
			for _, w := range []string{tc.ran, "Excluded tests (not run): 3. Excluded tests are not covered.", requiredClosing, plainSteps, "no current approval"} {
				if !strings.Contains(summary, w) {
					t.Errorf("summary lacks %q:\n%s", w, summary)
				}
			}
			if title != "Awaiting owner approval" || strings.Contains(summary, usualClosing) {
				t.Errorf("title %q, or the usual closing sentence, in:\n%s", title, summary)
			}
			if strings.Contains(summary, blocking) != tc.block {
				t.Errorf("the sentence about a blocking test is present = %v, want %v:\n%s", !tc.block, tc.block, summary)
			}
			if tc.block {
				counts, sentence, approve := strings.Index(summary, tc.ran), strings.Index(summary, blocking), strings.Index(summary, "To approve this exact revision")
				if counts < 0 || sentence < counts || approve < sentence {
					t.Errorf("the sentence does not follow the counts and come before the approve command (%d, %d, %d):\n%s", counts, sentence, approve, summary)
				}
			}
		})
	}
}

// A summary says nothing of a run that is pending or unavailable: with no counts the approval is missing, as it always read.
func TestCheckSummaryOfAMissingApprovalWithoutCountsIsTheUsualOne(t *testing.T) {
	_, summary := checkText(t, awaiting(nil, false))
	if !strings.Contains(summary, usualClosing) || strings.Contains(summary, requiredClosing) || strings.Contains(summary, "tests ran") || strings.Contains(summary, blocking) {
		t.Errorf("summary:\n%s", summary)
	}
}

func TestCheckSummaryOfAMissingApprovalWithCountsAndAModeHoldsBoth(t *testing.T) {
	_, summary := checkText(t, awaiting(ran(), true))
	for _, w := range []string{modeCommand, blocking, "14 tests ran", requiredClosing} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
}

// A summary is public: no test name, no path, no link.
func TestCheckSummaryOfAMissingApprovalWithCountsHoldsNoRepositoryText(t *testing.T) {
	name := regexp.MustCompile(`Test[A-Z]\w*|_test\.go|\.go\b|/`)
	for _, c := range []commandtext.Check{awaiting(passed(), false), awaiting(ran(), true)} {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c.Tier = tier
			title, summary := checkText(t, c)
			for _, s := range []string{title, summary} {
				if s == "" || strings.Contains(s, "<!--") || strings.Contains(s, "@") || strings.Contains(s, "http") || len(s) > 4096 {
					t.Errorf("the output is empty or holds a marker, a mention or a link, or is long: %q", s)
				}
			}
			// The only slashes are the ones of the commands.
			if found := name.FindAllString(strings.ReplaceAll(summary, "/suiteward ", ""), -1); len(found) > 0 {
				t.Errorf("the summary holds %q that looks like a test name or a path:\n%s", found, summary)
			}
		}
	}
}

func TestCheckSummaryRefusesAModeAndCountsThatDoNotFitAMissingApproval(t *testing.T) {
	with := func(c commandtext.Check, edit func(*commandtext.Check)) commandtext.Check { edit(&c); return c }
	mode := func(c *commandtext.Check) { c.ApproveMode = true }
	for name, c := range map[string]commandtext.Check{
		"a mode on a success":                  with(check(commandtext.CheckSuccess, "ready"), mode),
		"a mode on a pending canonical run":    with(requiredCheck("canonical-pending"), mode),
		"a mode on a runner block":             with(check(commandtext.CheckInProgress, "runner-uncovered"), mode),
		"a mode on a first contract":           with(awaiting(nil, true), func(c *commandtext.Check) { c.Version = 0 }),
		"counts on a first contract":           with(awaiting(passed(), false), func(c *commandtext.Check) { c.Version = 0 }),
		"counts that are not the sum of a run": with(awaiting(nil, false), func(c *commandtext.Check) { c.Execution = &commandtext.CheckExecution{Tests: 5, Passed: 2} }),
		"an unblock on a missing approval":     with(awaiting(passed(), false), func(c *commandtext.Check) { c.Unblocked = true }),
		"counts on a rejected owner command":   with(check(commandtext.CheckFailure, "owner-command-rejected"), func(c *commandtext.Check) { c.Execution = ran() }),
	} {
		t.Run(name, func(t *testing.T) { mustPanic(t, func() { commandtext.CheckSummary(c) }) })
	}
}
