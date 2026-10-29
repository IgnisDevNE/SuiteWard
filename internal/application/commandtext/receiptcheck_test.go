package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The summary of the SuiteWard / Receipt check (docs/contracts/runtime.md, "The SuiteWard / Receipt check"): the counts of a receipt that completed, in the words of the results
// comment, the tier, the evidence sentence, for a receipt that was not run the reason in words, and the unblock. No repository text, no path, no test name, no link and no mode.

const (
	evidenceText         = "What the test runner saw is evidence, not a verdict."
	receiptUnblockedText = "The owner unblocked this commit. Its receipt could not run, so the tests of this change did not run."
)

// receiptBanned matches what a public summary never holds: a mode, a link, a path, a test name, an HTML comment, a mention or a command.
var receiptBanned = regexp.MustCompile(`(?i)\b(mode|link|paths?|https?|tests? names?)\b|<!--|@|#issuecomment|/suiteward`)

// receiptReasons are the reasons of the Receipt check, each with its state, its title and a phrase only its text holds.
var receiptReasons = []struct {
	state  commandtext.CheckState
	reason string
	title  string
	phrase string
	name   string
}{
	{commandtext.CheckInProgress, "receipt-running", "Receipt pending", "has not finished. The tests are waiting for the test runner, or they are running", "in_progress"},
	{commandtext.CheckInProgress, "receipt-not-responding", "Receipt waiting: test runner not responding", "has not answered for about half a minute", "in_progress"},
	{commandtext.CheckFailure, "receipt-failing", "Receipt: failing or missing tests", "fails or gives no result with this change", "failure"},
	{commandtext.CheckSuccess, "receipt-complete", "Receipt ready", "ran the tests that this change adds or edits", "success"},
	{commandtext.CheckNeutral, "receipt-not-run", "Receipt unavailable", "could not run the receipt of this commit", "neutral"},
	{commandtext.CheckNeutral, "receipt-not-applicable", "No receipt for this revision", "This commit has no receipt", "neutral"},
}

// seen is the receipt of a head: what it saw. Only a receipt that completed has any.
var seen = execution.ReceiptCounts{Detects: 3, Inert: 2, NewAPI: 1, Skipped: 4, Excluded: 5, Flaky: 6}

func receiptOf(reason string) commandtext.ReceiptCheck {
	for _, tc := range receiptReasons {
		if tc.reason != reason {
			continue
		}
		c := commandtext.ReceiptCheck{State: tc.state, Reason: reason, Proposal: 3, Revision: 2, Tier: commandtext.Isolated}
		switch reason {
		case "receipt-failing":
			c.Counts, c.Markers = execution.ReceiptCounts{Detects: 3, Inert: 2, NewAPI: 1, Fails: 2, Missing: 1, Flaky: 6, Skipped: 4, Excluded: 5, Unparsed: 7}, 9
		case "receipt-complete":
			c.Counts, c.Markers = seen, 8
		case "receipt-not-run":
			c.Unavailable = "no-executor"
		}
		return c
	}
	panic("unknown reason " + reason)
}

func receiptText(t *testing.T, c commandtext.ReceiptCheck) (title, summary string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked: %v", c.Reason, r)
		}
	}()
	return commandtext.ReceiptCheckSummary(c)
}

func TestReceiptCheckTitlesAreTheOnesOfTheContract(t *testing.T) {
	titles := map[string]string{}
	for _, tc := range receiptReasons {
		title, _ := receiptText(t, receiptOf(tc.reason))
		if title != tc.title {
			t.Errorf("%s: title %q, want %q", tc.reason, title, tc.title)
		}
		if other, dup := titles[title]; dup {
			t.Errorf("%s and %s share the title %q", tc.reason, other, title)
		}
		titles[title] = tc.reason
	}
}

func TestReceiptCheckSummaryStatesTheStateTheRevisionAndTheTier(t *testing.T) {
	for _, tc := range receiptReasons {
		_, summary := receiptText(t, receiptOf(tc.reason))
		for _, w := range []string{tc.phrase, "- State: `" + tc.name + "`", "- Proposal revision: P3-R2", "- Installation type: on a separate server"} {
			if !strings.Contains(summary, w) {
				t.Errorf("%s: summary lacks %q:\n%s", tc.reason, w, summary)
			}
		}
		if strings.Contains(summary, "unblocked") || strings.Contains(summary, "Why it") {
			t.Errorf("%s: the summary says something of an unblock or of a reason that it has not:\n%s", tc.reason, summary)
		}
	}
}

// Every reason but receipt-not-applicable says that the results are evidence; a withdrawn receipt observed nothing, so it does not.
func TestReceiptCheckSummaryStatesTheEvidenceSentenceExceptWhereNothingWasObserved(t *testing.T) {
	for _, tc := range receiptReasons {
		_, summary := receiptText(t, receiptOf(tc.reason))
		if has := strings.Contains(summary, evidenceText); has != (tc.reason != "receipt-not-applicable") {
			t.Errorf("%s: the evidence sentence is there: %v\n%s", tc.reason, has, summary)
		}
	}
}

const (
	receiptRunningText  = "The receipt of this commit, the run of the tests that this change adds or edits, has not finished. The tests are waiting for the test runner, or they are running. This check has no result yet."
	receiptCompleteText = "SuiteWard ran the tests that this change adds or edits, with the change and on the code before it. The counts below are what it saw."
	receiptEmptyText    = "The receipt of this commit is empty, so there is nothing to judge."
)

// The text of a reason is the first paragraph of its summary. A complete receipt with no evaluated test (every state count at zero) says that it is empty; any state
// count makes it a receipt of tests that ran. Excluded tests and marker lines are not evaluated tests.
func TestReceiptCheckSummaryOpensWithTheTextOfItsReason(t *testing.T) {
	opens := func(t *testing.T, c commandtext.ReceiptCheck, text string) {
		t.Helper()
		_, summary := receiptText(t, c)
		if first, _, _ := strings.Cut(summary, "\n\n"); first != text {
			t.Errorf("%s %+v: the first paragraph is %q, want %q", c.Reason, c.Counts, first, text)
		}
	}
	opens(t, receiptOf("receipt-running"), receiptRunningText)
	for name, counts := range map[string]execution.ReceiptCounts{"nothing": {}, "only excluded tests": {Excluded: 5}} {
		c := receiptOf("receipt-complete")
		c.Counts, c.Markers = counts, 4
		t.Run("empty receipt with "+name, func(t *testing.T) { opens(t, c, receiptEmptyText) })
	}
	for name, counts := range map[string]execution.ReceiptCounts{
		"a test that detects": {Detects: 1}, "an inert test": {Inert: 1}, "a new API test": {NewAPI: 1}, "a flaky test": {Flaky: 1}, "a skipped test": {Skipped: 1}, "every state": seen,
	} {
		c := receiptOf("receipt-complete")
		c.Counts = counts
		t.Run("receipt with "+name, func(t *testing.T) { opens(t, c, receiptCompleteText) })
	}
}

func TestReceiptCheckSummaryStatesEveryCountInTheWordsOfTheResultsComment(t *testing.T) {
	_, complete := receiptText(t, receiptOf("receipt-complete"))
	for _, w := range []string{
		"Tests this change adds or edits: 16.\n" +
			"- Fail before the change and pass now, so they show the change works: 3\n" +
			"- Pass before and after, so they show nothing: 2\n" +
			"- Use code that the old code lacks, so they could not run before the change: 1\n" +
			"- Fail with this change: 0\n" +
			"- Give no result: 0\n" +
			"- Give different results when run again: 6\n" +
			"- Skipped: 4\n\n" +
			"5 tests that this change adds or edits cannot be run by SuiteWard and are not covered. Lines to look at: 8.\n\n",
	} {
		if !strings.Contains(complete, w) {
			t.Errorf("summary lacks %q:\n%s", w, complete)
		}
	}
	_, failing := receiptText(t, receiptOf("receipt-failing"))
	for _, w := range []string{
		"Tests this change adds or edits: 19.\n" +
			"- Fail before the change and pass now, so they show the change works: 3\n" +
			"- Pass before and after, so they show nothing: 2\n" +
			"- Use code that the old code lacks, so they could not run before the change: 1\n" +
			"- Fail with this change: 2\n" +
			"- Give no result: 1\n" +
			"- Give different results when run again: 6\n" +
			"- Skipped: 4\n\n" +
			"5 tests that this change adds or edits cannot be run by SuiteWard and are not covered. Changed test files that cannot be read: 7. Lines to look at: 9.\n\n",
	} {
		if !strings.Contains(failing, w) {
			t.Errorf("summary lacks %q:\n%s", w, failing)
		}
	}
}

// A receipt that completed with one excluded test says it in the singular, and a note that has nothing to say is left out.
func TestReceiptCheckSummaryLeavesOutTheNotesThatHaveNothingToSay(t *testing.T) {
	c := receiptOf("receipt-complete")
	c.Counts, c.Markers = execution.ReceiptCounts{Detects: 1, Excluded: 1}, 0
	_, summary := receiptText(t, c)
	if !strings.Contains(summary, "- Skipped: 0\n\n1 test that this change adds or edits cannot be run by SuiteWard and is not covered.\n\n") || strings.Contains(summary, "Lines to look at") ||
		strings.Contains(summary, "cannot be read") {
		t.Errorf("summary:\n%s", summary)
	}
}

// Only a receipt that completed has counts. An empty one says that it adds or edits no test that SuiteWard can run, and a receipt that is running, was not run or does not
// apply says nothing of counts, which would read as zero tests.
func TestReceiptCheckSummaryOfAReceiptThatDidNotCompleteHasNoCountsAndAnEmptyOneSaysSo(t *testing.T) {
	for _, reason := range []string{"receipt-running", "receipt-not-responding", "receipt-not-run", "receipt-not-applicable"} {
		_, summary := receiptText(t, receiptOf(reason))
		for _, s := range []string{"Tests this change adds or edits", "This change adds or edits no test", "Lines to look at", "cannot be read", "not covered"} {
			if strings.Contains(summary, s) {
				t.Errorf("%s: the summary has counts, which a receipt that did not complete has not (%q):\n%s", reason, s, summary)
			}
		}
	}
	c := receiptOf("receipt-complete")
	c.Counts, c.Markers = execution.ReceiptCounts{}, 0
	if _, summary := receiptText(t, c); !strings.Contains(summary, "\n\nThis change adds or edits no test that SuiteWard can run.\n\n") || strings.Contains(summary, "Tests this change adds or edits") {
		t.Errorf("an empty receipt:\n%s", summary)
	}
}

// unavailableCodes are the reasons a job is unavailable (docs/contracts/runtime.md, "The reason a run is unavailable"), whose sentences testwords_test.go pins.
var unavailableCodes = []string{"executor-error", "sandbox-unavailable", "archive-unavailable", "lease-expired", "timeout", "github-unavailable", "no-executor", "fetch-failed",
	"install-failed", "resource-limit", "toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported", "executor-not-configured", "no-merge-base"}

func TestReceiptCheckSummaryOfAnUnavailableReceiptSaysWhyInTheWordsOfTheContract(t *testing.T) {
	for _, code := range unavailableCodes {
		sentence, ok := commandtext.UnavailableSentence(code)
		if !ok {
			t.Fatalf("no sentence for %q", code)
		}
		c := receiptOf("receipt-not-run")
		c.Unavailable = code
		_, summary := receiptText(t, c)
		if !strings.Contains(summary, "SuiteWard could not run the receipt of this commit.\n\n"+sentence+"\n\n"+receiptNotRunNext+receiptNotRunUnblock+"\n\n- State: `neutral`") {
			t.Errorf("%s: summary lacks %q after the text of the reason:\n%s", code, sentence, summary)
		}
		if strings.Contains(summary, code) {
			t.Errorf("%s: the summary says the code, not the words:\n%s", code, summary)
		}
	}
}

const (
	receiptNotRunNext    = "This check does not fail the pull request. A new push runs the receipt again."
	receiptNotRunUnblock = " Where the receipt is required, the owner can unblock this commit. The comment on the pull request gives the command."
)

func TestReceiptCheckSummaryOfAnUnblockedHeadSaysThatTheTestsDidNotRun(t *testing.T) {
	c := receiptOf("receipt-not-run")
	c.Unblocked = true
	_, summary := receiptText(t, c)
	if !strings.Contains(summary, receiptUnblockedText) || !strings.Contains(summary, "- State: `neutral`") || !strings.Contains(summary, receiptNotRunNext+"\n\n") || strings.Contains(summary, "can unblock") {
		t.Errorf("summary:\n%s", summary)
	}
}

func TestReceiptCheckSummaryStatesTheTierAndHoldsNoRepositoryTextNoPathNoLinkAndNoMode(t *testing.T) {
	for _, tc := range receiptReasons {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c := receiptOf(tc.reason)
			c.Tier = tier
			if tc.reason == "receipt-not-run" {
				c.Unblocked = true
			}
			title, summary := receiptText(t, c)
			if tier == commandtext.CoLocated && (!strings.Contains(summary, "on the same computer as your coding agent") || !strings.Contains(summary, "protection is reduced")) {
				t.Errorf("%s: the co-located summary lacks the tier or the reduced assurance:\n%s", tc.reason, summary)
			}
			if tier == commandtext.Isolated && strings.Contains(summary, "reduced") {
				t.Errorf("%s: the isolated summary states a reduced assurance:\n%s", tc.reason, summary)
			}
			for _, s := range []string{title, summary} {
				if s == "" || receiptBanned.MatchString(s) || len(s) > 4096 {
					t.Errorf("%s: the output is empty, long or holds %q: %q", tc.reason, receiptBanned.FindString(s), s)
				}
			}
		}
	}
}

// The summary of an empty receipt is a text of its own: it holds the same nothing, and none of the phrases that the owner's rule removes.
func TestReceiptCheckSummaryOfAnEmptyReceiptHoldsNoRepositoryTextAndDoesNotSayThatNoTestsRan(t *testing.T) {
	c := receiptOf("receipt-complete")
	c.Counts, c.Markers = execution.ReceiptCounts{}, 0
	title, summary := receiptText(t, c)
	for _, s := range []string{title, summary} {
		if receiptBanned.MatchString(s) {
			t.Errorf("the output holds %q: %q", receiptBanned.FindString(s), s)
		}
		for _, phrase := range notRunPhrases {
			if strings.Contains(s, phrase) {
				t.Errorf("the output says %q: %q", phrase, s)
			}
		}
	}
}

func TestReceiptCheckSummaryWithoutAReferenceNamesNone(t *testing.T) {
	c := receiptOf("receipt-not-applicable")
	c.Proposal, c.Revision = 0, 0
	if _, summary := receiptText(t, c); strings.Contains(summary, "Proposal revision") {
		t.Errorf("the summary names a revision that does not exist:\n%s", summary)
	}
}

func TestReceiptCheckSummaryRefusesWhatCannotExist(t *testing.T) {
	with := func(reason string, edit func(*commandtext.ReceiptCheck)) commandtext.ReceiptCheck {
		c := receiptOf(reason)
		edit(&c)
		return c
	}
	for name, c := range map[string]commandtext.ReceiptCheck{
		"an unknown reason":                                 with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Reason = "receipt-green" }),
		"no reason":                                         with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Reason = "" }),
		"a state that is none":                              with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.State = 0 }),
		"a running receipt that succeeded":                  with("receipt-running", func(c *commandtext.ReceiptCheck) { c.State = commandtext.CheckSuccess }),
		"a failing receipt that is neutral":                 with("receipt-failing", func(c *commandtext.ReceiptCheck) { c.State = commandtext.CheckNeutral }),
		"a complete receipt that failed":                    with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.State = commandtext.CheckFailure }),
		"an unrun receipt that is in progress":              with("receipt-not-run", func(c *commandtext.ReceiptCheck) { c.State = commandtext.CheckInProgress }),
		"no tier":                                           with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Tier = 0 }),
		"a revision without a proposal":                     with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Proposal = 0 }),
		"a revision out of range":                           with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Revision = 1_000_000_000 }),
		"a failing receipt that blocks nothing":             with("receipt-failing", func(c *commandtext.ReceiptCheck) { c.Counts = execution.ReceiptCounts{Detects: 1, Flaky: 2} }),
		"a complete receipt with a failing test":            with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Counts.Fails = 1 }),
		"a complete receipt with a missing test":            with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Counts.Missing = 1 }),
		"a complete receipt with a file that is not parsed": with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Counts.Unparsed = 1 }),
		"counts on a running receipt":                       with("receipt-running", func(c *commandtext.ReceiptCheck) { c.Counts.Detects = 1 }),
		"counts on an unrun receipt":                        with("receipt-not-run", func(c *commandtext.ReceiptCheck) { c.Counts.Inert = 1 }),
		"counts on a withdrawn receipt":                     with("receipt-not-applicable", func(c *commandtext.ReceiptCheck) { c.Counts.Excluded = 1 }),
		"marker lines on a withdrawn receipt":               with("receipt-not-applicable", func(c *commandtext.ReceiptCheck) { c.Markers = 1 }),
		"a negative count":                                  with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Counts.Inert = -1 }),
		"a negative number of marker lines":                 with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Markers = -1 }),
		"an unrun receipt without a reason":                 with("receipt-not-run", func(c *commandtext.ReceiptCheck) { c.Unavailable = "" }),
		"an unrun receipt of an unknown reason":             with("receipt-not-run", func(c *commandtext.ReceiptCheck) { c.Unavailable = "the-moon" }),
		"a reason on a complete receipt":                    with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Unavailable = "no-executor" }),
		"a reason on a withdrawn receipt":                   with("receipt-not-applicable", func(c *commandtext.ReceiptCheck) { c.Unavailable = "no-executor" }),
		"an unblock of a complete receipt":                  with("receipt-complete", func(c *commandtext.ReceiptCheck) { c.Unblocked = true }),
		"an unblock of a running receipt":                   with("receipt-running", func(c *commandtext.ReceiptCheck) { c.Unblocked = true }),
		"an unblock of a withdrawn receipt":                 with("receipt-not-applicable", func(c *commandtext.ReceiptCheck) { c.Unblocked = true }),
	} {
		t.Run(name, func(t *testing.T) { mustPanic(t, func() { commandtext.ReceiptCheckSummary(c) }) })
	}
}

// The Contract check has no neutral state: the summary of a reason of the Contract check refuses it.
func TestCheckSummaryRefusesTheNeutralState(t *testing.T) {
	for _, tc := range checkReasons {
		c := check(tc.state, tc.reason)
		c.State = commandtext.CheckNeutral
		mustPanic(t, func() { commandtext.CheckSummary(c) })
	}
}

const (
	receiptNotRespondingText = "The test runner has not answered for about half a minute, so the receipt of this commit has not run. SuiteWard keeps trying. This check has no result yet."
	receiptUnblockOffer      = "To approve without the receipt, the owner can comment `/suiteward unblock <code>`."
)

// A receipt that waits for a runner that does not respond says so, keeps the evidence sentence and the tier, and offers the unblock only where the receipt gate would
// refuse an approve (the caller says so): the offer ends the text of the reason.
func TestReceiptCheckSummaryOfARunnerThatDoesNotRespondOffersTheUnblockOnlyWhereTheGateRefuses(t *testing.T) {
	_, plain := receiptText(t, receiptOf("receipt-not-responding"))
	if !strings.Contains(plain, receiptNotRespondingText+"\n\n") || strings.Contains(plain, "unblock") || !strings.Contains(plain, evidenceText) {
		t.Errorf("the summary without the offer:\n%s", plain)
	}
	c := receiptOf("receipt-not-responding")
	c.OffersUnblock = true
	_, offered := receiptText(t, c)
	if !strings.Contains(offered, receiptNotRespondingText+" "+receiptUnblockOffer+"\n\n") || !strings.Contains(offered, evidenceText) || !strings.Contains(offered, "- Installation type: on a separate server") {
		t.Errorf("the summary with the offer:\n%s", offered)
	}
	for _, phrase := range notRunPhrases {
		if strings.Contains(offered, phrase) || strings.Contains(plain, phrase) {
			t.Errorf("the summary says %q", phrase)
		}
	}
}

func TestReceiptCheckSummaryRefusesAnUnblockOfferWhereThereIsNoRunnerWaiting(t *testing.T) {
	for _, reason := range []string{"receipt-running", "receipt-failing", "receipt-complete", "receipt-not-run", "receipt-not-applicable"} {
		c := receiptOf(reason)
		c.OffersUnblock = true
		mustPanic(t, func() { commandtext.ReceiptCheckSummary(c) })
	}
}
