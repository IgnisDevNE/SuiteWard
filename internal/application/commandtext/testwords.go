package commandtext

import (
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The sentence tables of the results of a run (docs/contracts/runtime.md, "The reason in words, and the meaning of the states"): the reason of a flagged test, the
// meaning of each state, the reason a mode is not met and the reason a run is unavailable. They are the reference wording of the pull request page and of the results
// comment. Nothing an executor sends reaches a text except through these tables: a sentence is chosen by a code and, for a test that varied, built from the
// observations of its runs, which are a closed set.

// The sentences that both tables hold. The contract writes the receipt's "with this change" as "…" after the canonical sentence, so the receipt's is the canonical
// one with those words added (a choice of task T1 of M2.1b, with the same words in the receipt's sentence of a test that varied).
const (
	noResultText     = "The test gave no result. The run may have stopped before it, or the test is not in the code."
	buildRuleText    = "A build rule, such as a tag or a file name like `_windows_test.go`, leaves this test out of the test run."
	nestedModuleText = "The test is in a folder with its own `go.mod`, which the test run does not enter."
	failedStem       = "The test failed or did not finish"
	buildErrorStem   = "The code did not build"
	runsPlaceholder  = "<runs>"
)

type testReasonKey struct {
	plan execution.PlanKind
	code execution.TestReasonCode
}

var testSentences = map[testReasonKey]string{
	{execution.PlanCanonical, execution.TestReasonFailed}:          failedStem + ".",
	{execution.PlanReceipt, execution.TestReasonFailed}:            failedStem + " with this change.",
	{execution.PlanCanonical, execution.TestReasonBuildError}:      buildErrorStem + ", so the test could not run.",
	{execution.PlanReceipt, execution.TestReasonBuildError}:        buildErrorStem + " with this change, so the test could not run.",
	{execution.PlanCanonical, execution.TestReasonNoResult}:        noResultText,
	{execution.PlanReceipt, execution.TestReasonNoResult}:          noResultText,
	{execution.PlanReceipt, execution.TestReasonNoBaseResult}:      "The test gave no result on the code before the change.",
	{execution.PlanCanonical, execution.TestReasonVaried}:          "The result changed from run to run: " + runsPlaceholder + ".",
	{execution.PlanReceipt, execution.TestReasonVaried}:            "The result changed from run to run with this change: " + runsPlaceholder + ".",
	{execution.PlanReceipt, execution.TestReasonBaseVaried}:        "On the code before the change the result changed from run to run: " + runsPlaceholder + ".",
	{execution.PlanReceipt, execution.TestReasonNewAPI}:            "The test passes with this change. The code before the change could not build it, because it uses something new.",
	{execution.PlanReceipt, execution.TestReasonInert}:             "The test passes with this change. On the code before the change it passed or skipped itself, so it does not show that the change does anything.",
	{execution.PlanCanonical, execution.TestReasonBuildConstraint}: buildRuleText,
	{execution.PlanReceipt, execution.TestReasonBuildConstraint}:   buildRuleText,
	{execution.PlanCanonical, execution.TestReasonNestedModule}:    nestedModuleText,
	{execution.PlanReceipt, execution.TestReasonNestedModule}:      nestedModuleText,
}

// runWords are the words of the observations that a run shows.
var runWords = map[execution.Observation]string{
	execution.ObservedPass:       "passed",
	execution.ObservedFail:       "failed",
	execution.ObservedBuildError: "did not build",
	execution.ObservedSkip:       "skipped",
	execution.ObservedAbsent:     "gave no result",
}

// TestReasonSentence says in words why a test is flagged or excluded. plan says which table to read, since a receipt says "with this change" where a canonical run
// does not. ok is false for a code that the plan has no sentence for, and for a reason that varied with no run, or with a run that no word names.
func TestReasonSentence(plan execution.PlanKind, r execution.TestReason) (sentence string, ok bool) {
	sentence, ok = testSentences[testReasonKey{plan, r.Code}]
	if !ok || !strings.Contains(sentence, runsPlaceholder) {
		return sentence, ok
	}
	if len(r.Runs) == 0 {
		return "", false
	}
	words := make([]string, len(r.Runs))
	for i, o := range r.Runs {
		if words[i], ok = runWords[o]; !ok {
			return "", false
		}
	}
	return strings.Replace(sentence, runsPlaceholder, strings.Join(words, ", "), 1), true
}

// StateMeaning is the meaning of one state, as the page shows it. Canonical and Receipt say which kind of run uses the state.
type StateMeaning struct {
	State              string // PASS, FAIL, SKIPPED, MISSING, FLAKY, EXCLUDED, DETECTS, INERT, NEW API or FAILS
	Canonical, Receipt bool
	Meaning            string
}

var stateMeanings = []StateMeaning{
	{"PASS", true, false, "The approved test passed."},
	{"FAIL", true, false, "The approved test failed, did not finish, or its code did not build."},
	{"SKIPPED", true, true, "The test skipped itself, so it checked nothing."},
	{"MISSING", true, true, "SuiteWard expected a result from this test and got none."},
	{"FLAKY", true, true, "The test gave different results when it ran more than once."},
	{"EXCLUDED", true, true, "SuiteWard cannot run this test, so it is not covered. The reason is on its row."},
	{"DETECTS", false, true, "The test passes with this change and fails on the code before it. It shows that the change does something."},
	{"INERT", false, true, "The test passes with this change. On the code before the change it passed or skipped itself. It does not show that the change does anything."},
	{"NEW API", false, true, "The test passes with this change, but the code before it could not build the test, because it uses something new."},
	{"FAILS", false, true, "The test does not pass with this change: it failed, did not finish, or did not build."},
}

// StateMeanings are the meanings of the ten states, in the order that the page lists them.
func StateMeanings() []StateMeaning { return slices.Clone(stateMeanings) }

type modeReasonKey struct {
	mode   execution.Mode
	reason execution.Reason
}

const blockingTestsText = "A test fails, is missing or is flaky, or a changed test file cannot be read."

var modeNotMetSentences = map[modeReasonKey]string{
	{execution.ModeFix, execution.ReasonBlockingTests}:        blockingTestsText,
	{execution.ModeFeature, execution.ReasonBlockingTests}:    blockingTestsText,
	{execution.ModeRefactor, execution.ReasonBlockingTests}:   blockingTestsText,
	{execution.ModeFix, execution.ReasonNoEvaluatedTests}:     "This change adds or edits no test, so no test shows that it fixes something.",
	{execution.ModeFix, execution.ReasonNoDetection}:          "No test fails without the change, so none shows that it fixes something.",
	{execution.ModeFeature, execution.ReasonNoEvaluatedTests}: "This change adds or edits no test, so no test shows that it adds something.",
	{execution.ModeFeature, execution.ReasonNoDetection}:      "No test fails without the change or needs new code, so none shows that it adds something.",
	{execution.ModeRefactor, execution.ReasonNotInert}:        "A test behaves differently with the change, so this does not look like a refactor.",
}

// ModeNotMetSentence says in words why the summary of a mode is not met, for a reason of the domain; ok is false for a mode and a reason that make no row.
func ModeNotMetSentence(mode execution.Mode, reason execution.Reason) (sentence string, ok bool) {
	sentence, ok = modeNotMetSentences[modeReasonKey{mode, reason}]
	return sentence, ok
}

var unavailableSentences = map[string]string{
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

// UnavailableSentence says in words why a run is unavailable, for a reason code of the closed set; ok is false for any other.
func UnavailableSentence(code string) (sentence string, ok bool) {
	sentence, ok = unavailableSentences[code]
	return sentence, ok
}
