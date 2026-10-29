package commandtext

import (
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The acknowledgments of the execution commands (M2.1) that are not a refusal of a pull request: the receipt gate's refusal of an approve, the preview
// mode and the unblock. They state only fixed text, the owner's login, a reference, a mode and counts: no repository text.

// codeUsedAgain ends the text of a refusal that consumed the code of its command: a code works once.
const codeUsedAgain = "The code was used, so the next attempt needs the next code: wait for your authenticator app to show a new one."

// executionAck renders ReceiptRefused, ModeSet and Unblocked; ValidateAck has run, so the fields fit the outcome.
func executionAck(a Ack) string {
	who := "@" + a.Owner
	switch a.Outcome {
	case ReceiptRefused:
		return receiptRefusal(a)
	case ModeSet:
		return paragraphs(
			"Preview mode recorded for this pull request by "+who+": `"+a.Mode.String()+"`.",
			"This is only a preview. The pull request page shows it, but no check and no approval uses it. The mode that counts is the one you write in the approve command.",
		)
	case Unblocked:
		return paragraphs(
			"Unblock recorded by "+who+" for the current commit of this pull request. Its checks no longer wait for "+plansWords(a.Plans)+"."+stoppedWaiting(a.Ended),
			"The checks will say that the owner unblocked this commit and that its tests did not run. The unblock changes no result: tests that have finished, whether they pass or fail, stay as they are.",
			"A new push is a new commit. If its tests cannot run either, it needs a new unblock.",
		)
	}
	return notInGroup(a.Outcome)
}

// stoppedWaiting is the sentence for an unblock that ended an open run, because the test runner was not responding: empty when it ended none.
func stoppedWaiting(ended Plans) string {
	if ended == (Plans{}) {
		return ""
	}
	return " SuiteWard stopped waiting for the test runner, which was not responding."
}

// receiptRefusal renders the refusal of an approve by the receipt gate: why, what to do, and that the code was used (it was verified and consumed first,
// so that nobody can take it from the refused comment and post it again with another mode).
func receiptRefusal(a Ack) string {
	reason, next := gateText(a)
	parts := []string{"Your approval of " + mustReference(a) + " by @" + a.Owner + " was refused: " + reason + ". The code was used and nothing was approved."}
	return paragraphs(append(append(parts, next...), "The next attempt needs the next code: wait for your authenticator app to show a new one.")...)
}

// gateText is why the gate refused an approve and the paragraphs that say what to do next.
func gateText(a Ack) (reason string, next []string) {
	switch a.Gate {
	case execution.ReasonReceiptPending:
		return "the receipt of this commit has not finished yet (the receipt is the run of the tests that this change adds or edits)",
			[]string{"Wait for the `SuiteWard / Receipt` check to finish, then comment the approve again."}
	case execution.ReasonReceiptUnavailable:
		return "the tests of the receipt could not run on this commit",
			[]string{
				"To run the tests again, push a new commit. To approve this commit without a receipt instead, comment:", copyable("/suiteward unblock <code>"),
				"Then comment the approve again, with the next code. An unblock applies only to tests that could not run, or that wait for a test runner that is not responding.",
			}
	case execution.ReasonModeRequired:
		return "this repository requires a mode with the approve, and your comment named none",
			[]string{"To approve, say what kind of change this is (its mode) and comment:", modeApproveCommand(Reference(a.Proposal, a.Revision))}
	case execution.ReasonModeNotMet:
		next = append([]string{"The `" + a.Mode.String() + "` mode needs " + modeNeeds(a.Mode) + "."}, countsText(a)...)
		return "the receipt of this commit does not meet the `" + a.Mode.String() + "` mode",
			append(next, "To go on, push a commit whose receipt meets the mode, or approve with the mode that describes this change.")
	}
	panic(fmt.Sprintf("commandtext: unknown gate reason %q", a.Gate))
}

// modeNeeds is what the receipt must show for the summary of mode to be met (ADR 0028, section 6).
func modeNeeds(mode execution.Mode) string {
	const noBlock = ", and no test that fails, gives no result or gives different results when run again"
	switch mode {
	case execution.ModeFix:
		return "at least one test that fails without the change and passes with it" + noBlock
	case execution.ModeFeature:
		return "at least one test that passes with the change and either fails without it or needs new code" + noBlock
	case execution.ModeRefactor:
		return "every test of the receipt to pass with the change, and to pass or skip itself on the code before it"
	}
	panic(fmt.Sprintf("commandtext: unknown mode %d", mode))
}

// countsText states the receipt of the commit in the words of the results comment, then the tests that SuiteWard cannot run, which that comment leaves to its page.
// A changed test file that cannot be read blocks every mode, so that is said when there are some.
func countsText(a Ack) []string {
	parts := receiptText(ReceiptRun{Counts: a.Counts, Markers: a.Markers})
	if a.Counts.Unparsed != 0 {
		parts = append(parts, "Changed test files that cannot be read block every mode.")
	}
	if a.Counts.Excluded != 0 {
		parts = append(parts, fmt.Sprintf("Tests that SuiteWard cannot run, so the receipt does not cover them: %d.", a.Counts.Excluded))
	}
	return parts
}

// modeApproveCommand shows the approve of ref with a mode to copy.
func modeApproveCommand(ref string) string {
	return "```\n/suiteward approve " + ref + " <fix|feature|refactor> <code>\n```\n\n" +
		"Replace `<fix|feature|refactor>` with the mode of this change and `<code>` with a current code from your authenticator app."
}
