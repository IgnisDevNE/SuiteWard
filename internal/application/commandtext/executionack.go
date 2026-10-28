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
			"The receipt page shows it. It is a preview: no check and no approval reads it. The mode that counts travels in the approve command, with its code.",
		)
	case Unblocked:
		return paragraphs(
			"Unblock recorded by "+who+" for the current head of this pull request. SuiteWard lifted what was unavailable on it: "+liftedPlans(a.Plans)+"."+stoppedWaiting(a.Ended),
			"The checks will state that the owner unblocked this head and that the tests did not run. The unblock changes no result: a run that is pending, failing or complete stays as it is.",
			"A new push is a new head and needs a new unblock if its run is unavailable too.",
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

// liftedPlans names the runs an unblock lifted.
func liftedPlans(p Plans) string {
	switch {
	case p.Canonical && p.Receipt:
		return "its canonical run and its receipt"
	case p.Canonical:
		return "its canonical run"
	}
	return "its receipt"
}

// receiptRefusal renders the refusal of an approve by the receipt gate: why, what to do, and that the code was used (it was verified and consumed first,
// so that nobody can take it from the refused comment and post it again with another mode).
func receiptRefusal(a Ack) string {
	reason, next := gateText(a)
	parts := []string{"Approval of " + mustReference(a) + " by @" + a.Owner + " was refused by the receipt gate: " + reason + ". The code was used and nothing was approved."}
	return paragraphs(append(append(parts, next...), "The next attempt needs the next code: wait for your authenticator app to show a new one.")...)
}

// gateText is why the gate refused an approve and the paragraphs that say what to do next.
func gateText(a Ack) (reason string, next []string) {
	switch a.Gate {
	case execution.ReasonReceiptPending:
		return "the receipt of this head is not complete yet",
			[]string{"Wait for the `SuiteWard / Receipt` check to finish, then comment the approve again."}
	case execution.ReasonReceiptUnavailable:
		return "the receipt of this head is unavailable",
			[]string{
				"Push a new head to run the tests again, or, to approve this head without a receipt, comment:", copyable("/suiteward unblock <code>"),
				"Then comment the approve again, with the next code. An unblock lifts only a run that is unavailable on the current head.",
			}
	case execution.ReasonModeRequired:
		return "this repository requires a mode with the approve, and the comment named none",
			[]string{"Name what this change is, with a mode:", modeApproveCommand(Reference(a.Proposal, a.Revision))}
	case execution.ReasonModeNotMet:
		return "the receipt of this head does not meet the `" + a.Mode.String() + "` mode",
			[]string{
				"The `" + a.Mode.String() + "` mode needs " + modeNeeds(a.Mode) + ".",
				countsLine(a),
				"Push a head whose receipt meets the mode, or approve with the mode that describes this change.",
			}
	}
	panic(fmt.Sprintf("commandtext: unknown gate reason %q", a.Gate))
}

// modeNeeds is what the receipt must show for the summary of mode to be met (ADR 0028, section 6).
func modeNeeds(mode execution.Mode) string {
	const noBlock = "and no test that FAILS, is MISSING or is FLAKY"
	switch mode {
	case execution.ModeFix:
		return "at least one test that detects the change (DETECTS) " + noBlock
	case execution.ModeFeature:
		return "at least one test that detects the change or covers a new API (DETECTS or NEW API) " + noBlock
	case execution.ModeRefactor:
		return "every evaluated test to be INERT"
	}
	panic(fmt.Sprintf("commandtext: unknown mode %d", mode))
}

// countsLine states the receipt of the head, the counts of each state, the marker lines seen and, when there are some, the changed test files that do not parse, which
// block every mode.
func countsLine(a Ack) string {
	c := a.Counts
	line := fmt.Sprintf("Receipt of this head: %d DETECTS, %d INERT, %d NEW API, %d FAILS, %d MISSING, %d FLAKY, %d SKIPPED. Excluded tests: %d. Marker lines seen: %d.",
		c.Detects, c.Inert, c.NewAPI, c.Fails, c.Missing, c.Flaky, c.Skipped, c.Excluded, a.Markers)
	if c.Unparsed != 0 {
		line += fmt.Sprintf(" Changed test files that do not parse: %d.", c.Unparsed)
	}
	return line
}

// modeApproveCommand shows the approve of ref with a mode to copy.
func modeApproveCommand(ref string) string {
	return "```\n/suiteward approve " + ref + " <fix|feature|refactor> <code>\n```\n\n" +
		"Replace `<fix|feature|refactor>` with the mode of this change and `<code>` with a current code from your authenticator app."
}
