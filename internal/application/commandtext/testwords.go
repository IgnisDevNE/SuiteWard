package commandtext

import "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"

// The sentence tables of the results of a run (docs/contracts/runtime.md, "The reason in words, and the meaning of the states"): the reason of a flagged test, the
// meaning of each state, the reason a mode is not met and the reason a run is unavailable. They are the reference wording of the pull request page and of the results
// comment. Nothing an executor sends reaches a text except through these tables: a sentence is chosen by a code and, for a test that varied, built from the
// observations of its runs, which are a closed set.

// TestReasonSentence says in words why a test is flagged or excluded. plan says which table to read, since a receipt says "with this change" where a canonical run
// does not. ok is false for a code that the plan has no sentence for, and for a reason that varied with no run, or with a run that no word names.
func TestReasonSentence(plan execution.PlanKind, r execution.TestReason) (sentence string, ok bool) {
	return "", false
}

// StateMeaning is the meaning of one state, as the page shows it. Canonical and Receipt say which kind of run uses the state.
type StateMeaning struct {
	State              string // PASS, FAIL, SKIPPED, MISSING, FLAKY, EXCLUDED, DETECTS, INERT, NEW API or FAILS
	Canonical, Receipt bool
	Meaning            string
}

// StateMeanings are the meanings of the ten states, in the order that the page lists them.
func StateMeanings() []StateMeaning { return nil }

// ModeNotMetSentence says in words why the summary of a mode is not met, for a reason of the domain; ok is false for a mode and a reason that make no row.
func ModeNotMetSentence(mode execution.Mode, reason execution.Reason) (sentence string, ok bool) {
	return "", false
}

// UnavailableSentence says in words why a run is unavailable, for a reason code of the closed set; ok is false for any other.
func UnavailableSentence(code string) (sentence string, ok bool) { return "", false }
