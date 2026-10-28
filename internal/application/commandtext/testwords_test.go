package commandtext_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The sentence tables of the results of a run (docs/contracts/runtime.md, "The reason in words, and the meaning of the states"), checked word for word: they are
// the reference wording of the pull request page and of the results comment.

const (
	canonicalRun = execution.PlanCanonical
	receiptRun   = execution.PlanReceipt

	seenPass  = execution.ObservedPass
	seenFail  = execution.ObservedFail
	seenBuild = execution.ObservedBuildError
	seenSkip  = execution.ObservedSkip
	seenNone  = execution.ObservedAbsent
)

func reasonOf(code execution.TestReasonCode, runs ...execution.Observation) execution.TestReason {
	return execution.TestReason{Code: code, Runs: runs}
}

func TestTestReasonSentence(t *testing.T) {
	const (
		failed      = "The test failed or did not finish"
		noBuild     = "The code did not build"
		noResult    = "The test gave no result. The run may have stopped before it, or the test is not in the code."
		newAPI      = "The test passes with this change. The code before the change could not build it, because it uses something new."
		inert       = "The test passes with this change. On the code before the change it passed or skipped itself, so it does not show that the change does anything."
		buildRule   = "A build rule, such as a tag or a file name like `_windows_test.go`, leaves this test out of the test run."
		nestedAbove = "The test is in a folder with its own `go.mod`, which the test run does not enter."
	)
	tests := []struct {
		name string
		plan execution.PlanKind
		in   execution.TestReason
		want string
	}{
		{"a canonical test failed", canonicalRun, reasonOf(execution.TestReasonFailed), failed + "."},
		{"a receipt test failed with this change", receiptRun, reasonOf(execution.TestReasonFailed), failed + " with this change."},
		{"a canonical test did not build", canonicalRun, reasonOf(execution.TestReasonBuildError), noBuild + ", so the test could not run."},
		{"a receipt test did not build with this change", receiptRun, reasonOf(execution.TestReasonBuildError), noBuild + " with this change, so the test could not run."},
		{"no result, canonical", canonicalRun, reasonOf(execution.TestReasonNoResult), noResult},
		{"no result, receipt", receiptRun, reasonOf(execution.TestReasonNoResult), noResult},
		{"no result on the base", receiptRun, reasonOf(execution.TestReasonNoBaseResult), "The test gave no result on the code before the change."},
		{"a canonical test that varied", canonicalRun, reasonOf(execution.TestReasonVaried, seenPass, seenFail, seenPass), "The result changed from run to run: passed, failed, passed."},
		{"a receipt test that varied with this change", receiptRun, reasonOf(execution.TestReasonVaried, seenPass, seenFail),
			"The result changed from run to run with this change: passed, failed."},
		{"a receipt test that varied on the base, in every word", receiptRun, reasonOf(execution.TestReasonBaseVaried, seenPass, seenFail, seenBuild, seenSkip, seenNone),
			"On the code before the change the result changed from run to run: passed, failed, did not build, skipped, gave no result."},
		{"new API", receiptRun, reasonOf(execution.TestReasonNewAPI), newAPI},
		{"inert", receiptRun, reasonOf(execution.TestReasonInert), inert},
		{"a build rule, canonical", canonicalRun, reasonOf(execution.TestReasonBuildConstraint), buildRule},
		{"a build rule, receipt", receiptRun, reasonOf(execution.TestReasonBuildConstraint), buildRule},
		{"a nested module, canonical", canonicalRun, reasonOf(execution.TestReasonNestedModule), nestedAbove},
		{"a nested module, receipt", receiptRun, reasonOf(execution.TestReasonNestedModule), nestedAbove},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := commandtext.TestReasonSentence(tc.plan, tc.in); !ok || got != tc.want {
				t.Errorf("TestReasonSentence = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestTestReasonSentenceRefusesWhatItHasNoWordsFor(t *testing.T) {
	tests := []struct {
		name string
		plan execution.PlanKind
		in   execution.TestReason
	}{
		{"a base reason of a canonical run", canonicalRun, reasonOf(execution.TestReasonNoBaseResult)},
		{"a base variation of a canonical run", canonicalRun, reasonOf(execution.TestReasonBaseVaried, seenPass, seenFail)},
		{"new API of a canonical run", canonicalRun, reasonOf(execution.TestReasonNewAPI)},
		{"inert of a canonical run", canonicalRun, reasonOf(execution.TestReasonInert)},
		{"a code of no table", receiptRun, reasonOf("something-else")},
		{"no code", receiptRun, reasonOf("")},
		{"a variation with no run", receiptRun, reasonOf(execution.TestReasonVaried)},
		{"a base variation with no run", receiptRun, reasonOf(execution.TestReasonBaseVaried)},
		{"a variation with a run no word names", receiptRun, reasonOf(execution.TestReasonVaried, seenPass, execution.ObservedExcluded)},
		{"a variation with a run that is none", receiptRun, reasonOf(execution.TestReasonVaried, seenPass, 0)},
		{"a plan that is none", 0, reasonOf(execution.TestReasonFailed)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := commandtext.TestReasonSentence(tc.plan, tc.in); ok || got != "" {
				t.Errorf("TestReasonSentence = %q, %v; want no sentence", got, ok)
			}
		})
	}
}

func meaning(state string, canonical, receipt bool, words string) commandtext.StateMeaning {
	return commandtext.StateMeaning{State: state, Canonical: canonical, Receipt: receipt, Meaning: words}
}

func TestStateMeanings(t *testing.T) {
	want := []commandtext.StateMeaning{
		meaning("PASS", true, false, "The approved test passed."),
		meaning("FAIL", true, false, "The approved test failed, did not finish, or its code did not build."),
		meaning("SKIPPED", true, true, "The test skipped itself, so it checked nothing."),
		meaning("MISSING", true, true, "SuiteWard expected a result from this test and got none."),
		meaning("FLAKY", true, true, "The test gave different results when it ran more than once."),
		meaning("EXCLUDED", true, true, "SuiteWard cannot run this test, so it is not covered. The reason is on its row."),
		meaning("DETECTS", false, true, "The test passes with this change and fails on the code before it. It shows that the change does something."),
		meaning("INERT", false, true, "The test passes with this change. On the code before the change it passed or skipped itself. It does not show that the change does anything."),
		meaning("NEW API", false, true, "The test passes with this change, but the code before it could not build the test, because it uses something new."),
		meaning("FAILS", false, true, "The test does not pass with this change: it failed, did not finish, or did not build."),
	}
	if got := commandtext.StateMeanings(); !slices.Equal(got, want) {
		t.Errorf("StateMeanings() = %v\nwant %v", got, want)
	}
}

// Each state of the domain has a row, and the row says which runs use it.
func TestStateMeaningsCoverTheStatesOfTheDomain(t *testing.T) {
	canonical, receipt := map[string]bool{}, map[string]bool{}
	for s := execution.CanonicalPass; s <= execution.CanonicalExcluded; s++ {
		canonical[s.String()] = true
	}
	for s := execution.ReceiptExcluded; s <= execution.ReceiptInert; s++ {
		receipt[strings.ReplaceAll(s.String(), "_", " ")] = true
	}
	rows := commandtext.StateMeanings()
	if len(rows) != len(canonical)+len(receipt)-4 { // MISSING, FLAKY, SKIPPED and EXCLUDED are in both
		t.Errorf("%d rows for %d canonical and %d receipt states", len(rows), len(canonical), len(receipt))
	}
	for _, row := range rows {
		if row.Canonical != canonical[row.State] || row.Receipt != receipt[row.State] || !(row.Canonical || row.Receipt) {
			t.Errorf("%s: canonical %v and receipt %v, but the domain has canonical %v and receipt %v", row.State, row.Canonical, row.Receipt, canonical[row.State], receipt[row.State])
		}
	}
}

func TestModeNotMetSentence(t *testing.T) {
	const blocking = "A test fails, is missing or is flaky, or a changed test file cannot be read."
	tests := []struct {
		mode   execution.Mode
		reason execution.Reason
		want   string
	}{
		{execution.ModeFix, execution.ReasonBlockingTests, blocking},
		{execution.ModeFeature, execution.ReasonBlockingTests, blocking},
		{execution.ModeRefactor, execution.ReasonBlockingTests, blocking},
		{execution.ModeFix, execution.ReasonNoEvaluatedTests, "This change adds or edits no test, so no test shows that it fixes something."},
		{execution.ModeFix, execution.ReasonNoDetection, "No test fails without the change, so none shows that it fixes something."},
		{execution.ModeFeature, execution.ReasonNoEvaluatedTests, "This change adds or edits no test, so no test shows that it adds something."},
		{execution.ModeFeature, execution.ReasonNoDetection, "No test fails without the change or needs new code, so none shows that it adds something."},
		{execution.ModeRefactor, execution.ReasonNotInert, "A test behaves differently with the change, so this does not look like a refactor."},
	}
	for _, tc := range tests {
		if got, ok := commandtext.ModeNotMetSentence(tc.mode, tc.reason); !ok || got != tc.want {
			t.Errorf("ModeNotMetSentence(%q, %q) = %q, %v; want %q", tc.mode, tc.reason, got, ok, tc.want)
		}
	}
	for _, tc := range []struct {
		mode   execution.Mode
		reason execution.Reason
	}{
		{execution.ModeNone, execution.ReasonBlockingTests},
		{execution.ModeFix, execution.ReasonNotInert},
		{execution.ModeFeature, execution.ReasonNotInert},
		{execution.ModeRefactor, execution.ReasonNoDetection},
		{execution.ModeRefactor, execution.ReasonNoEvaluatedTests},
		{execution.ModeFix, execution.ReasonModeNotMet},
		{execution.ModeFix, execution.ReasonNone},
	} {
		if got, ok := commandtext.ModeNotMetSentence(tc.mode, tc.reason); ok || got != "" {
			t.Errorf("ModeNotMetSentence(%q, %q) = %q, %v; want none", tc.mode, tc.reason, got, ok)
		}
	}
}

// Every reason that a summary can give for a mode has a sentence: Summarize is run over every combination of counts being zero or not, and the eight pairs of a mode
// and a reason that it gives are the rows of the table.
func TestEveryReasonThatASummaryGivesHasASentence(t *testing.T) {
	type pair struct {
		mode   execution.Mode
		reason execution.Reason
	}
	given := map[pair]bool{}
	for bits := range 1 << 7 {
		on := func(i int) int { return bits >> i & 1 }
		counts := execution.ReceiptCounts{Detects: on(0), Inert: on(1), NewAPI: on(2), Fails: on(3), Missing: on(4), Flaky: on(5), Unparsed: on(6)}
		for _, mode := range []execution.Mode{execution.ModeFix, execution.ModeFeature, execution.ModeRefactor} {
			summary, err := counts.Summarize(mode)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Verdict == execution.VerdictNotMet {
				given[pair{mode, summary.Reason}] = true
			}
		}
	}
	if len(given) != 8 {
		t.Errorf("Summarize gave %d pairs of a mode and a reason, want 8: %v", len(given), given)
	}
	for p := range given {
		if got, ok := commandtext.ModeNotMetSentence(p.mode, p.reason); !ok || got == "" {
			t.Errorf("mode %q: no sentence for the reason %q", p.mode, p.reason)
		}
	}
}

func TestUnavailableSentence(t *testing.T) {
	want := map[string]string{
		"executor-error":          "The test runner failed in a way that SuiteWard cannot name.",
		"sandbox-unavailable":     "The test runner could not build the isolated space that the tests run in.",
		"archive-unavailable":     "The test runner could not read the files of this commit.",
		"lease-expired":           "The test runner stopped before it reported a result.",
		"timeout":                 "The run took longer than its time limit.",
		"github-unavailable":      "SuiteWard could not read this commit from GitHub for too long.",
		"no-executor":             "The test runner did not respond.",
		"fetch-failed":            "A Go dependency could not be downloaded.",
		"install-failed":          "The dependencies of the repository were refused before any download: a damaged `go.mod` or `go.sum`, or too many or too large.",
		"resource-limit":          "The tests used more memory or produced more output than the limit allows, and were stopped.",
		"toolchain-mismatch":      "The repository needs a newer Go than this SuiteWard release has.",
		"source-mismatch":         "The files of this commit did not match what GitHub lists for it, so SuiteWard did not trust them.",
		"too-large":               "The repository, a file or the result is larger than SuiteWard accepts.",
		"profile-unsupported":     "This test runner cannot run this kind of repository.",
		"executor-not-configured": "No test runner is set up for this SuiteWard installation.",
		"no-merge-base":           "GitHub has no common ancestor of this commit and the base branch, so there is nothing to compare with.",
	}
	for code, sentence := range want {
		if got, ok := commandtext.UnavailableSentence(code); !ok || got != sentence {
			t.Errorf("UnavailableSentence(%q) = %q, %v; want %q", code, got, ok, sentence)
		}
		// The closed set is the one the Receipt check already words.
		if _, ok := commandtext.UnavailableReason(code); !ok {
			t.Errorf("%q is not a code of the closed set of UnavailableReason", code)
		}
	}
	for _, code := range []string{"", "something-else", "No-Executor", "cancelled", "head-moved"} {
		if got, ok := commandtext.UnavailableSentence(code); ok || got != "" {
			t.Errorf("UnavailableSentence(%q) = %q, %v; want none", code, got, ok)
		}
	}
}

// The words are plain: no sentence says "executor" or "canonical", and none says that SuiteWard does not run tests.
func TestResultWordsArePlain(t *testing.T) {
	var all []string
	for _, row := range commandtext.StateMeanings() {
		all = append(all, row.Meaning)
	}
	for _, code := range []execution.TestReasonCode{execution.TestReasonFailed, execution.TestReasonBuildError, execution.TestReasonNoResult, execution.TestReasonNoBaseResult,
		execution.TestReasonVaried, execution.TestReasonBaseVaried, execution.TestReasonNewAPI, execution.TestReasonInert, execution.TestReasonBuildConstraint, execution.TestReasonNestedModule} {
		for _, plan := range []execution.PlanKind{canonicalRun, receiptRun} {
			if s, ok := commandtext.TestReasonSentence(plan, reasonOf(code, seenPass, seenFail)); ok {
				all = append(all, s)
			}
		}
	}
	for _, code := range []string{"executor-error", "sandbox-unavailable", "archive-unavailable", "lease-expired", "timeout", "github-unavailable", "no-executor", "fetch-failed",
		"install-failed", "resource-limit", "toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported", "executor-not-configured", "no-merge-base"} {
		s, _ := commandtext.UnavailableSentence(code)
		all = append(all, s)
	}
	if len(all) < 40 {
		t.Fatalf("only %d sentences were gathered", len(all))
	}
	for _, s := range all {
		lower := strings.ToLower(s)
		for _, banned := range append([]string{"executor", "canonical"}, notRunPhrases...) {
			if strings.Contains(lower, banned) {
				t.Errorf("%q says %q", s, banned)
			}
		}
	}
}
