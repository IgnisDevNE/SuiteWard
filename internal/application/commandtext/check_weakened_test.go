package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The weakened sentence of an approval-missing summary (docs/contracts/runtime.md, "The weakened signal", "Where it shows"): a test that this change edits and that fails
// in its approved version replaces the generic sentence of an approval that is missing, with a count and what to do.

const (
	weakenedOne  = "1 approved test that this change edits fails, is missing or is flaky on this commit, in the version that was approved. A test that is edited can pass by checking less. Read the edit before you approve."
	weakenedMany = "2 approved tests that this change edits fail, are missing or are flaky on this commit, in the version that was approved. A test that is edited can pass by checking less. Read the edit before you approve."
)

func awaitingWeakened(n int) commandtext.Check {
	c := awaiting(ran(), false)
	c.Weakened = n
	return c
}

func TestCheckSummaryOfAMissingApprovalStatesTheWeakenedTestsInPlaceOfTheGenericSentence(t *testing.T) {
	for name, tc := range map[string]struct {
		weakened int
		want     string
	}{"one test": {1, weakenedOne}, "two tests": {2, weakenedMany}} {
		t.Run(name, func(t *testing.T) {
			_, summary := checkText(t, awaitingWeakened(tc.weakened))
			if !strings.Contains(summary, tc.want) || strings.Contains(summary, blocking) {
				t.Errorf("the summary lacks the weakened sentence, or keeps the generic one:\n%s", summary)
			}
			counts, sentence, approve := strings.Index(summary, "14 tests ran"), strings.Index(summary, tc.want), strings.Index(summary, "To approve this exact revision")
			if counts < 0 || sentence < counts || approve < sentence {
				t.Errorf("the sentence does not follow the counts and come before the approve command (%d, %d, %d):\n%s", counts, sentence, approve, summary)
			}
		})
	}
}

// With a count of 0, or none known, the generic sentence is still true, so it stays.
func TestCheckSummaryOfAMissingApprovalKeepsTheGenericSentenceWhenNoTestIsWeakened(t *testing.T) {
	_, summary := checkText(t, awaitingWeakened(0))
	if !strings.Contains(summary, blocking) || strings.Contains(summary, "that this change edits fail") || strings.Contains(summary, "approved test that this change edits") {
		t.Errorf("the summary does not keep the generic sentence alone:\n%s", summary)
	}
}

func TestCheckSummaryRefusesAWeakenedCountThatDoesNotFitAMissingApproval(t *testing.T) {
	with := func(c commandtext.Check, edit func(*commandtext.Check)) commandtext.Check { edit(&c); return c }
	weakened := func(n int) func(*commandtext.Check) { return func(c *commandtext.Check) { c.Weakened = n } }
	for name, c := range map[string]commandtext.Check{
		"a negative count":                     awaitingWeakened(-1),
		"more tests than the run has blocking": awaitingWeakened(4),
		"a run with no blocking test":          with(awaiting(passed(), false), weakened(1)),
		"no counts":                            with(awaiting(nil, false), weakened(1)),
		"a failing canonical run":              with(requiredCheck("canonical-failing"), weakened(1)),
		"a success":                            with(check(commandtext.CheckSuccess, "ready"), weakened(1)),
		"a pending canonical run":              with(requiredCheck("canonical-pending"), weakened(1)),
		"the first contract, which has no run": with(check(commandtext.CheckInProgress, "approval-missing"), func(c *commandtext.Check) { c.Version, c.Weakened = 0, 1 }),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("the summary was rendered, want a refusal")
				}
			}()
			commandtext.CheckSummary(c)
		})
	}
}

func TestCheckSummaryOfAWeakenedHeadHoldsNoTestNameAndNoPath(t *testing.T) {
	name := regexp.MustCompile(`Test[A-Z]\w*|_test\.go|\.go\b|/`)
	for _, n := range []int{1, 2, 3} {
		_, summary := checkText(t, awaitingWeakened(n))
		if found := name.FindAllString(strings.ReplaceAll(summary, "/suiteward ", ""), -1); len(found) > 0 || strings.Contains(summary, "http") || strings.Contains(summary, "<!--") {
			t.Errorf("the summary holds %q, a link or a marker:\n%s", found, summary)
		}
	}
}
