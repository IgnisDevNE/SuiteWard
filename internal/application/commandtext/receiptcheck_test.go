package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The summary of the SuiteWard / Receipt check (docs/contracts/runtime.md, "The SuiteWard / Receipt check"): the count of each state, the excluded tests, the marker
// lines, the changed test files that do not parse, the tier, the evidence sentence, for a receipt that was not run the reason in words, and the unblock. No
// repository text, no path, no test name, no link and no mode.

const (
	evidenceText         = "Observed by this instance's executor. Evidence, not a verdict."
	receiptUnblockedText = "The owner unblocked this head: its receipt was unavailable, so the tests did not run."
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
	{commandtext.CheckInProgress, "receipt-running", "Receipt pending", "it is waiting for an executor, or it is running", "in_progress"},
	{commandtext.CheckFailure, "receipt-failing", "Receipt: failing or missing tests", "FAILS or is MISSING", "failure"},
	{commandtext.CheckSuccess, "receipt-complete", "Receipt ready", "ran the tests that this revision adds or changes", "success"},
	{commandtext.CheckNeutral, "receipt-not-run", "Receipt unavailable", "could not run the receipt of this head", "neutral"},
	{commandtext.CheckNeutral, "receipt-not-applicable", "No receipt for this revision", "has no receipt now", "neutral"},
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
		for _, w := range []string{tc.phrase, "- State: `" + tc.name + "`", "- Proposal revision: P3-R2", "- Installation tier: `isolated`"} {
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
	receiptRunningText  = "The receipt of this head has not finished: it is waiting for an executor, or it is running. The check has no result yet."
	receiptCompleteText = "SuiteWard ran the tests that this revision adds or changes, on the head and on the code before the change. The counts below are what it saw."
	receiptEmptyText    = "This revision adds or changes no test that SuiteWard can run, so the receipt is empty. The counts below are what it saw."
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

func TestReceiptCheckSummaryStatesEveryCount(t *testing.T) {
	_, complete := receiptText(t, receiptOf("receipt-complete"))
	for _, w := range []string{
		"- Tests by state: 3 DETECTS, 2 INERT, 1 NEW API, 0 FAILS, 0 MISSING, 6 FLAKY, 4 SKIPPED",
		"- Excluded tests: 5", "- Marker lines seen: 8", "- Changed test files that do not parse: 0",
	} {
		if !strings.Contains(complete, w) {
			t.Errorf("summary lacks %q:\n%s", w, complete)
		}
	}
	_, failing := receiptText(t, receiptOf("receipt-failing"))
	for _, w := range []string{
		"- Tests by state: 3 DETECTS, 2 INERT, 1 NEW API, 2 FAILS, 1 MISSING, 6 FLAKY, 4 SKIPPED",
		"- Excluded tests: 5", "- Marker lines seen: 9", "- Changed test files that do not parse: 7",
	} {
		if !strings.Contains(failing, w) {
			t.Errorf("summary lacks %q:\n%s", w, failing)
		}
	}
}

// An empty receipt is complete: a success with the counts at zero. Nothing that was not run has a count either.
func TestReceiptCheckSummaryOfAnEmptyOrAnUnrunReceiptHasItsCountsAtZero(t *testing.T) {
	for _, reason := range []string{"receipt-running", "receipt-not-run", "receipt-not-applicable"} {
		_, summary := receiptText(t, receiptOf(reason))
		if !strings.Contains(summary, "- Tests by state: 0 DETECTS, 0 INERT, 0 NEW API, 0 FAILS, 0 MISSING, 0 FLAKY, 0 SKIPPED") || !strings.Contains(summary, "- Excluded tests: 0") {
			t.Errorf("%s: the counts are not at zero:\n%s", reason, summary)
		}
	}
	c := receiptOf("receipt-complete")
	c.Counts, c.Markers = execution.ReceiptCounts{}, 0
	if _, summary := receiptText(t, c); !strings.Contains(summary, "- Tests by state: 0 DETECTS") || !strings.Contains(summary, "- Marker lines seen: 0") {
		t.Errorf("an empty receipt:\n%s", summary)
	}
}

// unavailableWords are the reasons a receipt job is unavailable (docs/contracts/runtime.md, "Unavailable reasons"), each with a phrase of its words.
var unavailableWords = map[string]string{
	"executor-error":          "the executor failed in a way that no other reason names",
	"sandbox-unavailable":     "the sandbox could not be built",
	"archive-unavailable":     "could not be read",
	"lease-expired":           "the executor was lost",
	"timeout":                 "time limit",
	"github-unavailable":      "GitHub could not be read",
	"no-executor":             "no executor was seen",
	"fetch-failed":            "could not be fetched from the Go module proxy",
	"install-failed":          "dependency prefetch was refused",
	"resource-limit":          "resource limit",
	"toolchain-mismatch":      "needs a Go toolchain that this release does not have",
	"source-mismatch":         "source of the head could not be verified",
	"too-large":               "size limit",
	"profile-unsupported":     "does not support the profile",
	"executor-not-configured": "no executor is configured",
	"no-merge-base":           "no merge base",
}

func TestReceiptCheckSummaryOfAnUnavailableReceiptSaysWhyInWords(t *testing.T) {
	for code, phrase := range unavailableWords {
		c := receiptOf("receipt-not-run")
		c.Unavailable = code
		_, summary := receiptText(t, c)
		if !strings.Contains(summary, phrase) {
			t.Errorf("%s: summary lacks %q:\n%s", code, phrase, summary)
		}
		if strings.Contains(summary, code) {
			t.Errorf("%s: the summary says the code, not the words:\n%s", code, summary)
		}
	}
}

func TestReceiptCheckSummaryOfAnUnblockedHeadSaysThatTheTestsDidNotRun(t *testing.T) {
	c := receiptOf("receipt-not-run")
	c.Unblocked = true
	_, summary := receiptText(t, c)
	if !strings.Contains(summary, receiptUnblockedText) || !strings.Contains(summary, "- State: `neutral`") {
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
			if tier == commandtext.CoLocated && (!strings.Contains(summary, "`co-located`") || !strings.Contains(summary, "assurance is reduced")) {
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
