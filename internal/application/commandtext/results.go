package commandtext

import (
	"errors"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The three comments of a head (docs/contracts/runtime.md, "The comments of a head"): the results comment, the not-responding comment and the runner-back comment.
// Each is built from counts and from closed sets of codes, so no test name, no path and no text of the repository or of the test runner can reach it.

// CanonicalRun is the canonical run that a results comment reports: complete, with its counts, or unavailable, with the code of its reason.
type CanonicalRun struct {
	Unavailable string // the reason code of an unavailable run, empty for a complete one
	Counts      execution.CanonicalCounts
}

// ReceiptRun is the receipt run that a results comment reports: complete, with its counts and the marker lines it saw, or unavailable, with the code of its reason.
type ReceiptRun struct {
	Unavailable string // the reason code of an unavailable run, empty for a complete one
	Counts      execution.ReceiptCounts
	Markers     int
}

// Results is what the results comment of a head states. Canonical and Receipt are the wanted runs, nil for one that is not wanted; at least one is set, and each is
// final. Proposal and Revision name the revision of the head (both zero for none), which the approve command needs. Unblocked holds the plans whose job the owner
// unblocked, and Ended those whose job the command ended: a subset of Unblocked. PullURL is the link to the pull request page, empty without a base address.
type Results struct {
	Head               string // the 40 lowercase hexadecimal digits of the head
	Pull               int
	Tier               Tier
	Proposal, Revision int
	CanonicalLevel     execution.CanonicalLevel
	ReceiptLevel       execution.ReceiptLevel
	Canonical          *CanonicalRun
	Receipt            *ReceiptRun
	Weakened           int
	Unblocked, Ended   Plans
	Next               execution.Next
	ApproveMode        bool // the approve command names a mode
	ApprovalMissing    bool // the reason of the head is that its approval is missing; the approve or its refusal is the next step then, and the unblock may be
	PullURL            string
}

// NotResponding is what the not-responding comment of a head states. Unblock says that the unblock is offered.
type NotResponding struct {
	Head    string
	Pull    int
	Unblock bool
	PullURL string
}

// RunnerBack is what the runner-back comment of a head states.
type RunnerBack struct {
	Head    string
	Pull    int
	PullURL string
}

const (
	noLinkText = "SuiteWard has no web address set, so this comment has no link. The checks of this commit carry the counts."

	resultsEvidence = "These results are what the test runner saw. They are evidence, not a verdict."
	informationText = "These results are for information. They do not hold this pull request."
	// unblockOffer is the sentence that offers the owner's unblock, in the results comment and in the not-responding comment.
	unblockOffer = "To go on without these tests, the owner can comment `/suiteward unblock <code>`. That lets this commit pass its checks without them. A new push runs them."
	// approvalText says what an approval does to approved tests that did not run: the approved head gets another run, under the approved version.
	approvalText = "After the owner approves this revision, SuiteWard runs the approved tests again, as the tests of the approved version."

	blockedText = "An approve would be refused now, because the receipt meets no kind of change, and a refused approve uses its code. To fix this, change the code or the tests and push a new commit."
	fixText     = "To fix this, change the code or the tests and push a new commit."
)

// ResultsComment renders the results comment of a head: the lead, the approved tests, the receipt, the kinds of change that it meets, the weakened count, the unblock,
// the level, the next step, the evidence sentence and the tier, and the link, each a paragraph that is left out when it does not apply. The values come from storage, so
// one that does not fit is an impossible state and panics.
func ResultsComment(r Results) string {
	must(validateResults(r))
	parts := []string{leadText(r.set().Lead(), r.Head)}
	if r.Canonical != nil {
		parts = append(parts, canonicalText(*r.Canonical))
	}
	if r.Receipt != nil {
		parts = append(parts, receiptText(*r.Receipt)...)
		parts = append(parts, kindsText(r)...)
	}
	if r.Weakened > 0 {
		parts = append(parts, WeakenedSentence(r.Weakened)+" The page names them.")
	}
	if unblock := unblockText(r); unblock != "" {
		parts = append(parts, unblock)
	}
	if r.CanonicalLevel != execution.CanonicalLevelRequired && r.ReceiptLevel != execution.ReceiptLevelRequired {
		parts = append(parts, informationText)
	}
	parts = append(parts, nextText(r)...)
	parts = append(parts, resultsEvidence)
	if r.Tier == CoLocated {
		parts = append(parts, CoLocatedNotice)
	}
	return paragraphs(append(parts, linkParts(r.PullURL)...)...)
}

// NotRespondingComment renders the not-responding comment of a head, which offers the unblock when a job that waits holds something back.
func NotRespondingComment(n NotResponding) string {
	must(validateHeadLink(n.Head, n.Pull, n.PullURL))
	parts := []string{
		"**The test runner is not responding.**",
		"The tests for this commit (" + shortHead(n.Head) + ") are waiting. SuiteWard has not heard from the test runner for about half a minute.",
		"This is a problem only if it lasts. SuiteWard keeps trying, and the tests start when the test runner answers. Check that it is running on the machine that runs SuiteWard. " +
			"If it stays silent, SuiteWard stops waiting after a while and says so.",
	}
	if n.Unblock {
		parts = append(parts, unblockOffer)
	}
	return paragraphs(append(parts, linkParts(n.PullURL)...)...)
}

// RunnerBackComment renders the runner-back comment of a head: the owner unblocked it, so its tests did not run and nothing will run them now.
func RunnerBackComment(b RunnerBack) string {
	must(validateHeadLink(b.Head, b.Pull, b.PullURL))
	return paragraphs(append([]string{
		"**The test runner is back.**",
		"The owner unblocked this commit (" + shortHead(b.Head) + ") while the test runner was not responding, so its tests did not run and its checks stopped waiting for them.",
		"Nothing will run them for this commit now. A new push runs the tests as usual.",
	}, linkParts(b.PullURL)...)...)
}

// shortHead is the first seven characters of a head, in a code span.
func shortHead(head string) string { return "`" + head[:7] + "`" }

// linkParts are the paragraphs of the link: the link to the pull request page and its note, or the sentence that there is none.
func linkParts(url string) []string {
	if url == "" {
		return []string{noLinkText}
	}
	return []string{"Details: [pull request page](" + url + ").", linkNote}
}

// set is the set that the results state, as the domain reads it, so that the lead follows from the counts and no value can state another.
func (r Results) set() execution.Set {
	s := execution.Set{CanonicalLevel: r.CanonicalLevel, ReceiptLevel: r.ReceiptLevel, ReceiptApplies: r.Receipt != nil, ReceiptDecided: true, Weakened: r.Weakened}
	if r.Canonical != nil {
		s.CanonicalJob, s.CanonicalCounts = execution.Job{ID: 1, State: jobState(r.Canonical.Unavailable)}, r.Canonical.Counts
	}
	if r.Receipt != nil {
		s.ReceiptJob, s.ReceiptCounts = execution.Job{ID: 2, State: jobState(r.Receipt.Unavailable)}, r.Receipt.Counts
	}
	return s
}

func jobState(unavailable string) execution.JobState {
	if unavailable == "" {
		return execution.JobComplete
	}
	return execution.JobUnavailable
}

func leadText(lead execution.Lead, head string) string {
	switch lead {
	case execution.LeadNotRun:
		return "The tests for this commit (" + shortHead(head) + ") did not run. SuiteWard could not run them."
	case execution.LeadPartial:
		return "SuiteWard ran some of the tests for this commit (" + shortHead(head) + "). Something needs your attention."
	case execution.LeadAttention:
		return "SuiteWard ran the tests for this commit (" + shortHead(head) + "). Something needs your attention."
	case execution.LeadClean:
		return "SuiteWard ran the tests for this commit (" + shortHead(head) + "). Nothing needs your attention."
	}
	panic(fmt.Sprintf("commandtext: no lead for the lead %d", lead))
}

// canonicalText states the approved tests: the tests that ran by outcome, the tests that were declared and cannot be run, or why the run did not happen.
func canonicalText(run CanonicalRun) string {
	if run.Unavailable != "" {
		return "Approved tests: not run. " + unavailableSentences[run.Unavailable]
	}
	c := run.Counts
	ran := c.Pass + c.Fail + c.Skipped + c.Missing + c.Flaky
	if ran+c.Excluded == 0 {
		return "Approved tests: none are declared, so none ran."
	}
	text := fmt.Sprintf("Approved tests: %s. %d passed. %d failed. %d gave no result. %d gave different results when they ran again. %s.",
		counted(ran, "test ran", "tests ran"), c.Pass, c.Fail, c.Missing, c.Flaky, counted(c.Skipped, "was skipped", "were skipped"))
	if c.Excluded > 0 {
		text += " " + counted(c.Excluded, "approved test cannot be run by SuiteWard and is not covered", "approved tests cannot be run by SuiteWard and are not covered") + "."
	}
	return text
}

// receiptText states the tests that the change adds or edits, by state, then the files that cannot be read and the lines to look at, or why the run did not happen.
func receiptText(run ReceiptRun) []string {
	if run.Unavailable != "" {
		return []string{"Tests this change adds or edits: not run. " + unavailableSentences[run.Unavailable]}
	}
	c := run.Counts
	var parts []string
	if c.Evaluated() == 0 {
		parts = append(parts, "This change adds or edits no test that SuiteWard can run.")
	} else {
		parts = append(parts, fmt.Sprintf("Tests this change adds or edits: %d.\n"+
			"- Fail before the change and pass now, so they show the change works: %d\n"+
			"- Pass before and after, so they show nothing: %d\n"+
			"- Use code that the old code lacks, so they could not run before the change: %d\n"+
			"- Fail with this change: %d\n"+
			"- Give no result: %d\n"+
			"- Give different results when run again: %d\n"+
			"- Skipped: %d", c.Evaluated(), c.Detects, c.Inert, c.NewAPI, c.Fails, c.Missing, c.Flaky, c.Skipped))
	}
	var notes []string
	if c.Unparsed > 0 {
		notes = append(notes, fmt.Sprintf("Changed test files that cannot be read: %d.", c.Unparsed))
	}
	if run.Markers > 0 {
		notes = append(notes, fmt.Sprintf("Lines to look at: %d. They are listed on the page.", run.Markers))
	}
	if len(notes) > 0 {
		parts = append(parts, strings.Join(notes, " "))
	}
	return parts
}

// kindsText says which kinds of change the receipt meets and why not, in the words of the page. A receipt that did not run, or has nothing to judge, meets none and says
// nothing.
func kindsText(r Results) []string {
	c := r.Receipt.Counts
	if r.Receipt.Unavailable != "" || c.Empty() {
		return nil
	}
	lines := []string{"Does the receipt meet each kind of change?"}
	for _, mode := range []execution.Mode{execution.ModeFix, execution.ModeFeature, execution.ModeRefactor} {
		summary, err := c.Summarize(mode)
		must(err)
		line := "- `" + mode.String() + "`: met."
		if summary.Verdict == execution.VerdictNotMet {
			sentence, ok := ModeNotMetSentence(mode, summary.Reason)
			if !ok {
				panic(fmt.Sprintf("commandtext: no sentence for the mode %s and the reason %q", mode, summary.Reason))
			}
			line = "- `" + mode.String() + "`: not met. " + sentence
		}
		lines = append(lines, line)
	}
	parts := []string{strings.Join(lines, "\n")}
	if r.ReceiptLevel == execution.ReceiptLevelRequired {
		parts = append(parts, "To approve, name a kind of change that is met.")
	}
	return parts
}

// unblockText is the paragraph of the unblock: what the owner unblocked and whether the command ended the wait, the offer where a run holds something back, and what an
// approval does to approved tests that did not run, which the payload says by ApprovalMissing (the next step may be the unblock where the receipt holds the approve too).
func unblockText(r Results) string {
	var sentences []string
	if r.Unblocked != (Plans{}) {
		sentences = append(sentences, "The owner unblocked this commit, so its checks do not wait for "+plansWords(r.Unblocked)+".")
		if r.Ended != (Plans{}) {
			sentences = append(sentences, "SuiteWard had stopped waiting for the test runner, which was not responding.")
		}
	}
	if r.Next == execution.NextUnblock {
		sentences = append(sentences, unblockOffer)
	}
	if r.ApprovalMissing && r.CanonicalLevel == execution.CanonicalLevelRequired && r.Canonical != nil && r.Canonical.Unavailable != "" {
		sentences = append(sentences, approvalText)
	}
	return strings.Join(sentences, " ")
}

func plansWords(p Plans) string {
	switch {
	case p.Canonical && p.Receipt:
		return "the approved tests and the receipt"
	case p.Canonical:
		return "the approved tests"
	}
	return "the receipt"
}

// nextText is the next step. The unblock is its own step, which unblockText gives.
func nextText(r Results) []string {
	switch r.Next {
	case execution.NextApprove:
		ref := Reference(r.Proposal, r.Revision)
		command := codeCommand("approve", ref)
		if r.ApproveMode {
			command = modeApproveCommand(ref)
		}
		return []string{"To approve this exact revision, comment:", command}
	case execution.NextBlocked:
		return []string{blockedText}
	case execution.NextFix:
		return []string{fixText}
	case execution.NextNone:
		return []string{"Nothing to do for now."}
	}
	return nil
}

// validateResults reports why ResultsComment would panic on r.
func validateResults(r Results) error {
	if err := validateHeadLink(r.Head, r.Pull, r.PullURL); err != nil {
		return err
	}
	switch {
	case r.Tier != Isolated && r.Tier != CoLocated:
		return fmt.Errorf("commandtext: unknown tier %d", r.Tier)
	case r.CanonicalLevel.String() == "" || r.ReceiptLevel.String() == "":
		return fmt.Errorf("commandtext: unknown levels %d and %d", r.CanonicalLevel, r.ReceiptLevel)
	case (r.Canonical != nil) != (r.CanonicalLevel != execution.CanonicalLevelOff):
		return errors.New("commandtext: the canonical run is reported exactly when its level is not off")
	case r.Receipt != nil && r.ReceiptLevel == execution.ReceiptLevelOff:
		return errors.New("commandtext: a receipt under a level that is off")
	case r.Weakened < 0:
		return fmt.Errorf("commandtext: a weakened count of %d", r.Weakened)
	case r.Next.String() == "":
		return fmt.Errorf("commandtext: unknown next step %d", r.Next)
	case r.Next == execution.NextApprove && r.Proposal == 0:
		return errors.New("commandtext: an approve for no revision")
	case (r.Next == execution.NextApprove || r.Next == execution.NextBlocked) && !r.ApprovalMissing:
		return errors.New("commandtext: an approve, or its refusal, for a head whose approval is not missing")
	case r.ApprovalMissing && (r.Next == execution.NextNone || r.Next == execution.NextFix):
		return errors.New("commandtext: a missing approval is the next step, or the unblock is: nothing to do and a fix to push are not")
	case (r.Proposal == 0) != (r.Revision == 0):
		return fmt.Errorf("commandtext: reference P%d-R%d does not fit", r.Proposal, r.Revision)
	case r.set().Lead() == 0:
		return errors.New("commandtext: no run is reported")
	}
	if r.Proposal != 0 {
		if _, err := reference(r.Proposal, r.Revision); err != nil {
			return err
		}
	}
	return errors.Join(validateRuns(r), validateUnblocks(r))
}

func validateHeadLink(head string, pull int, link string) error {
	switch {
	case !shaPattern.MatchString(head):
		return errors.New("commandtext: the head is not 40 lowercase hexadecimal digits")
	case pull < 1:
		return fmt.Errorf("commandtext: pull request %d", pull)
	case link != "" && !ValidPullLink(link, pull):
		return errors.New("commandtext: the link is not the link of the loopback page of this pull request")
	}
	return nil
}

// validateRuns reports a run whose reason is no code of the closed set, or that holds counts it cannot have: a run that did not run has none.
func validateRuns(r Results) error {
	var errs []error
	if c := r.Canonical; c != nil {
		n := c.Counts
		if min(n.Pass, n.Fail, n.Skipped, n.Missing, n.Flaky, n.Excluded) < 0 {
			errs = append(errs, errors.New("commandtext: a count of the approved tests is negative"))
		}
		errs = append(errs, validateUnavailable("approved tests", c.Unavailable, n != execution.CanonicalCounts{}))
	}
	if c := r.Receipt; c != nil {
		n := c.Counts
		if min(n.Excluded, n.Missing, n.Flaky, n.Skipped, n.Fails, n.NewAPI, n.Detects, n.Inert, n.Unparsed, c.Markers) < 0 {
			errs = append(errs, errors.New("commandtext: a count of the receipt is negative"))
		}
		errs = append(errs, validateUnavailable("receipt", c.Unavailable, n != execution.ReceiptCounts{} || c.Markers != 0))
	}
	return errors.Join(errs...)
}

func validateUnavailable(run, code string, counts bool) error {
	switch {
	case code == "":
		return nil
	case unavailableSentences[code] == "":
		return fmt.Errorf("commandtext: the %s did not run for a reason of no code of the closed set", run)
	case counts:
		return fmt.Errorf("commandtext: the %s did not run and has counts", run)
	}
	return nil
}

// validateUnblocks reports an unblock of a run that did not fail to run, which is the only run an owner can unblock, and an end that is no unblock.
func validateUnblocks(r Results) error {
	canonicalLost := r.Canonical != nil && r.Canonical.Unavailable != ""
	receiptLost := r.Receipt != nil && r.Receipt.Unavailable != ""
	switch {
	case (r.Unblocked.Canonical && !canonicalLost) || (r.Unblocked.Receipt && !receiptLost):
		return errors.New("commandtext: an unblocked run that did not fail to run, or is not reported")
	case (r.Ended.Canonical && !r.Unblocked.Canonical) || (r.Ended.Receipt && !r.Unblocked.Receipt):
		return errors.New("commandtext: a wait that ended for a run that is not unblocked")
	}
	return nil
}
