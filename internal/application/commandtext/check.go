package commandtext

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// CheckState is the conclusion of the SuiteWard / Contract check (D-CHECKS) and of the SuiteWard / Receipt check.
type CheckState uint8

const (
	CheckSuccess CheckState = iota + 1
	CheckInProgress
	CheckFailure
	CheckNeutral // the Receipt check alone: a receipt that was not run, or that no longer applies
)

// Check is what the output of a check run states. Reason is the stable code the check job stored, and State must
// be the one the code belongs to. Proposal and Revision name the revision the check concerns and are both zero
// when there is none; approval-missing requires them. Version (V<n>) is the canonical version, zero when there is
// none. Changes, which needs a revision, adds the numbers of the change report, without a path or a link: a check summary is public.
type Check struct {
	State              CheckState
	Reason             string
	Proposal, Revision int
	Version            int
	Tier               Tier
	// Paths are at most 20 of the PathsTotal runner files of a runner-uncovered check, each already cut by the caller; the other reasons have none.
	Paths      []string
	PathsTotal int
	Changes    *Changes
	// Execution and Unblocked mark the summary of canonical: required (docs/contracts/runtime.md, "The canonical gate"): the counts of a complete canonical run, and the
	// owner's unblock of an unavailable one. The reasons canonical-pending, canonical-not-responding, canonical-unavailable and canonical-failing mark it too. An approval that is missing against a canonical states the counts too.
	Execution *CheckExecution
	Unblocked bool
	// ApproveMode marks an approval-missing check whose approve names a mode: the repository requires a receipt (docs/contracts/runtime.md, "The revision comment"), as the
	// comment of the revision shows. It is false for the first contract, which has no canonical and so no receipt policy.
	ApproveMode bool
	// Weakened is the number of approved tests that this change edits and that fail, are missing or are flaky in their approved version, which an approval-missing check states in place of
	// the generic sentence (docs/contracts/runtime.md, "The weakened signal"). 0 is none, or none known.
	Weakened int
}

// CheckExecution are the counts of a complete canonical run: the tests that ran, how many passed, were skipped, failed, were missing or flaky, and how many are
// excluded and were not run.
type CheckExecution struct {
	Tests, Passed, Skipped, Failed, Missing, Flaky, Excluded int
}

// checkReason is the output of one reason code: its state, a title, what happened and the next action. The text
// never quotes a comment or the pull request. The only repository text a summary holds is the paths of a
// runner-uncovered check: at most 20, each cut by the caller and shown in a fence with its control characters
// escaped. A summary stays far below GitHub's limit of 65,535 characters.
type checkReason struct {
	state CheckState
	title string
	text  string
	next  string
}

var checkReasons = map[string]checkReason{
	"bootstrap-awaiting": {CheckInProgress, "No approved contract yet",
		"SuiteWard has no approved contract yet, so it cannot check this commit against one. This is not a problem with your change.",
		"Next: the owner comments `/suiteward propose` on the pull request that should carry the first contract. " +
			"To let this pull request merge without one, the owner comments `/suiteward skip <code>`, with a current code from the authenticator app."},
	"bootstrap-skipped": {CheckSuccess, "Skipped before the first contract",
		"The owner chose to skip this check before the first contract exists. SuiteWard checked nothing on this commit, so this success is not a contract result.",
		"Next: nothing to do. After the first contract exists, SuiteWard checks later pull requests against it."},
	"unchanged": {CheckSuccess, "No protected change",
		"This commit does not change the protected files. They match the approved contract.",
		"Next: nothing to do."},
	"ready": {CheckSuccess, "Ready for integration",
		"The owner approved this exact contract change, and SuiteWard's integrity check of this commit passed. This pull request is ready to merge.",
		"Next: merge this pull request when you are ready."},
	"bootstrap-ready": {CheckSuccess, "Bootstrap ready for integration",
		"The owner approved the first contract that this pull request proposes, and SuiteWard checked this commit. No approved contract exists yet. " +
			"After the merge, SuiteWard makes the first approved version from the merge commit.",
		"Next: merge this pull request when you are ready. A later push that changes the protected files needs a new approval."},
	"approval-missing": {CheckInProgress, "Awaiting owner approval",
		"This commit changes the protected contract, and the owner has not approved this exact revision of the change yet. This check waits until the owner approves.",
		"To approve this exact revision, comment:"},
	"not-current": {CheckInProgress, "Contract change is out of date",
		"This contract change is out of date: the approved contract, the policy or the change itself has moved on since it was made. SuiteWard checks it again on its next run.",
		"Next: nothing to do. If this stays in progress, update the branch from its base so SuiteWard can check the current contract."},
	"assessment-pending": {CheckInProgress, "Check not finished yet",
		"SuiteWard has not finished checking this commit. For example, GitHub may not have answered yet. This is not a problem with your change.",
		"Next: nothing to do. SuiteWard tries again on its next run."},
	"source-unavailable": {CheckInProgress, "Files cannot be read in full",
		"GitHub cut short the list of files in this commit, so SuiteWard cannot read all the protected files. SuiteWard cannot check this commit until GitHub gives the full list.",
		"Next: this check cannot pass until the repository has fewer files. Remove the files it does not need, then push a new commit."},
	"runner-uncovered": {CheckInProgress, "Runner files outside the protected scope",
		"This pull request adds or changes runner files, which control how tests run, and the protected scope does not cover them. " +
			"A runner file can change how the protected tests run without touching any of them. This commit is not ready until the owner decides.",
		"Next: choose one. To protect the files, commit the suggested declaration, which adds their runner patterns, and have the owner approve the new revision. " +
			"To leave them out, add their paths to `exclude` and have the owner approve that revision. SuiteWard gives both declarations in a comment on this pull request."},
	"fork-unsupported": {CheckInProgress, "Pull requests from forks are not supported",
		"SuiteWard does not support pull requests from forks yet, so it does not check this commit and never proposes a contract change from it.",
		"Next: open the pull request from a branch of this repository."},
	"invalid-source": {CheckFailure, "Contract files are invalid",
		"SuiteWard cannot use the contract declaration or the protected files of this commit. For example, `.suiteward.yml` may be invalid, " +
			"or the protected scope may reach a symlink. The approved contract stays in force.",
		"Next: fix `.suiteward.yml`, or remove the symlink, or point it to a regular file, and push. " +
			"You can also add the exact path of the symlink, from the top of the repository, to `exclude`. The owner must approve that, and nothing reached through the symlink stays protected."},
	"integrity-failed": {CheckFailure, "Approved contract failed its integrity check",
		"SuiteWard's stored copy of the approved contract failed its integrity check, so SuiteWard cannot trust it. " +
			"SuiteWard blocks every pull request of this repository until the copy is restored. Your change did not cause this.",
		"Next: the person who runs SuiteWard must restore SuiteWard's stored files from a backup, or set the repository up again. Pushing to this pull request does not help."},
	"owner-command-rejected": {CheckFailure, "Owner command rejected",
		"SuiteWard rejected the owner's latest command on this pull request. SuiteWard's reply to that command says why.",
		"Next: the owner comments the corrected command with a new code, or you push a new commit."},
	// The reasons of the canonical gate under canonical: required (docs/contracts/runtime.md, "The canonical gate"). Their summaries state counts and no repository text.
	"canonical-pending": {CheckInProgress, "Approved tests pending",
		"The approved tests have not finished on this commit. They are waiting for the test runner, or they are running. This check does not pass until they have run.",
		"Next: nothing to do. This check changes when the run ends."},
	"canonical-not-responding": {CheckInProgress, "Test runner not responding",
		"The test runner has not answered for about half a minute, so the approved tests have not run on this commit. This is a problem only if it lasts. " +
			"SuiteWard keeps trying, and the tests start when the test runner answers. This check does not pass until they have run.",
		"Next: check that the test runner is running on the machine that runs SuiteWard. To let this commit pass without the approved tests, the owner comments:"},
	"canonical-unavailable": {CheckInProgress, "Approved tests could not run",
		"SuiteWard could not run the approved tests on this commit, so this check does not pass.",
		"Next: push a new commit to run the approved tests again. To let this commit pass without the approved tests, the owner comments:"},
	"canonical-failing": {CheckFailure, "Approved tests failing",
		"At least one approved test fails, is missing or is flaky on this commit.",
		"Next: fix the code and push a new commit on which the approved tests pass."},
}

// canonicalReasons are the reasons of the canonical gate. They, the counts of a run and the owner's unblock mark the summary of canonical: required.
var canonicalReasons = []string{"canonical-pending", "canonical-not-responding", "canonical-unavailable", "canonical-failing"}

// unblockedText is what the summary says of a commit whose unavailable canonical run the owner unblocked.
const unblockedText = "The owner unblocked this commit. The approved tests could not run on it, so this check passes without them."

// executionText states the counts of a complete canonical run in the words of the results comment, and only the counts: no test name, no path. A run with no declared test ran no test.
func executionText(e CheckExecution) string {
	return canonicalText(CanonicalRun{Counts: execution.CanonicalCounts{Pass: e.Passed, Fail: e.Failed, Skipped: e.Skipped, Missing: e.Missing, Flaky: e.Flaky, Excluded: e.Excluded}})
}

// blockingEditText is what an approval that is missing says of a test that fails, is missing or is flaky on the commit: the canonical gate does not hold this check, but the owner
// should read the change before approving, because a change that edits a test can make it pass by checking less.
const blockingEditText = "At least one approved test fails, is missing or is flaky on this commit. If this change edits that test, read the edit before you approve. A test that is edited can pass by checking less."

// WeakenedSentence is what a text says of n approved tests that this change edits and that fail, are missing or are flaky in the version that was approved, and what to
// do (docs/contracts/runtime.md, "The weakened signal"): an edited test can pass by checking less. It is stated for a count above 0 only.
func WeakenedSentence(n int) string {
	if n < 1 {
		panic(fmt.Sprintf("commandtext: no weakened test to state, a count of %d", n))
	}
	count := "1 approved test that this change edits fails, is missing or is flaky on this commit, in the version that was approved."
	if n > 1 {
		count = fmt.Sprintf("%d approved tests that this change edits fail, are missing or are flaky on this commit, in the version that was approved.", n)
	}
	return count + " A test that is edited can pass by checking less. Read the edit before you approve."
}

// blockingEditSentence is the sentence that follows the counts of an approval that is missing when a test blocks: the weakened count and what to do when the change edits an
// approved test that fails in its approved version, and the generic sentence when none is known to be, which is still true.
func blockingEditSentence(weakened int) string {
	if weakened > 0 {
		return WeakenedSentence(weakened)
	}
	return blockingEditText
}

// closingText ends a summary: the usual one reports on the protected contract only, and the one of canonical: required reports on the run of the approved tests too.
func closingText(c Check) string {
	if c.Execution != nil || c.Unblocked || slices.Contains(canonicalReasons, c.Reason) {
		return "This check reports on the protected contract and on the run of its approved tests."
	}
	return "This check reports on the protected contract only."
}

// CheckSummary renders the output title and summary of a check run: the state and what it means, the revision
// and the canonical version when there are any, the installation tier and the next action. It never says that
// tests ran, except for the summary of canonical: required, which a canonical-* reason, the counts of a run or the owner's unblock mark: it states the counts,
// or that the owner unblocked the head and the approved tests did not run on it (docs/contracts/runtime.md, "What a text says about running tests"). The
// reason, the state and the tier come from storage, so a value that does not fit is an impossible state and panics.
func CheckSummary(c Check) (title, summary string) {
	reason, err := validateCheck(c)
	must(err)
	facts := []string{"- State: `" + checkStateName[c.State] + "`"}
	var ref string
	if c.Proposal != 0 {
		ref = Reference(c.Proposal, c.Revision)
		facts = append(facts, "- Proposal revision: "+ref)
	}
	if c.Version != 0 {
		facts = append(facts, fmt.Sprintf("- Approved version: V%d", c.Version))
	}
	facts = append(facts, tierLine(c.Tier))
	if c.Changes != nil {
		facts = append(facts, changeFacts(*c.Changes, false)...)
	}
	text := reason.text
	if c.Reason == "approval-missing" && c.Version == 0 { // no canonical: the head proposes the first contract, it changes none
		text = "This pull request proposes the first contract (" + ref + "). It waits for the owner to approve this exact revision."
	}
	parts := []string{text}
	if len(c.Paths) > 0 {
		parts = append(parts, listingAndMore("Runner files that this commit adds or changes outside the protected scope", c.Paths, c.PathsTotal))
	}
	parts = append(parts, strings.Join(facts, "\n"))
	if c.Changes != nil {
		parts = append(parts, "Marker lines are lines to look at, not a verdict.")
	}
	if c.Execution != nil {
		parts = append(parts, executionText(*c.Execution))
		if c.Reason == "approval-missing" && c.Execution.Failed+c.Execution.Missing+c.Execution.Flaky > 0 {
			parts = append(parts, blockingEditSentence(c.Weakened))
		}
	}
	if c.Unblocked {
		parts = append(parts, unblockedText)
	}
	if c.Tier == CoLocated {
		parts = append(parts, CoLocatedNotice)
	}
	next, command := reason.next, ""
	switch {
	case c.Reason == "approval-missing" && c.ApproveMode: // the receipt gate refuses an approve with no mode under a required receipt, and consumes its code
		next, command = "To approve this exact revision, say what kind of change it is (its mode) and comment:", modeApproveCommand(ref)
	case c.Reason == "approval-missing":
		command = codeCommand("approve", ref)
	case c.Reason == "canonical-unavailable", c.Reason == "canonical-not-responding":
		command = copyable("/suiteward unblock <code>")
	}
	parts = append(parts, next)
	if command != "" {
		parts = append(parts, command)
	}
	parts = append(parts, closingText(c))
	return reason.title, paragraphs(parts...)
}

// checkRequired reports why the counts, the unblock and the canonical reasons of c do not fit together: the counts of a complete run go with a success (which no
// test blocks), with canonical-failing (which one does) or with an approval that is missing against a canonical (which a test may block: the approval is what is missing), the
// unblock with a success and nothing else, and a pending or unavailable run has no counts.
func checkRequired(c Check) error {
	success := c.Reason == "ready" || c.Reason == "unchanged"
	var blocking int
	if c.Execution != nil {
		e := *c.Execution
		if min(e.Tests, e.Passed, e.Skipped, e.Failed, e.Missing, e.Flaky, e.Excluded) < 0 || e.Tests != e.Passed+e.Skipped+e.Failed+e.Missing+e.Flaky {
			return errors.New("commandtext: the counts of a canonical run are negative or do not add up")
		}
		blocking = e.Failed + e.Missing + e.Flaky
	}
	switch {
	case c.Execution != nil && !success && c.Reason != "canonical-failing" && c.Reason != "approval-missing":
		return fmt.Errorf("commandtext: reason %q has no counts of a canonical run", c.Reason)
	case c.Execution != nil && c.Reason == "approval-missing" && c.Version == 0:
		return errors.New("commandtext: a head with no canonical version has no canonical run to count")
	case c.Unblocked && (!success || c.Execution != nil):
		return fmt.Errorf("commandtext: reason %q does not fit an unblocked run, which is lifted only to a success and has no counts", c.Reason)
	case c.Weakened < 0 || c.Weakened > blocking:
		return fmt.Errorf("commandtext: %d weakened tests, which the %d blocking tests of the run do not show", c.Weakened, blocking)
	case c.Weakened > 0 && c.Reason != "approval-missing":
		return fmt.Errorf("commandtext: reason %q states no weakened test: only an approval that is missing does", c.Reason)
	case success && blocking > 0:
		return errors.New("commandtext: a success with a failing, missing or flaky test")
	case c.Reason == "canonical-failing" && blocking == 0:
		return errors.New("commandtext: canonical-failing needs counts that show a failing, missing or flaky test")
	}
	return nil
}

// validateCheck reports why CheckSummary would panic on c, and returns the output of its reason.
func validateCheck(c Check) (checkReason, error) {
	reason, known := checkReasons[c.Reason]
	if !known || reason.state != c.State {
		return reason, fmt.Errorf("commandtext: check reason %q does not fit state %d", c.Reason, c.State)
	}
	if err := errors.Join(checkCheckReference(c), checkRequired(c)); err != nil {
		return reason, err
	}
	switch {
	case c.Reason != "runner-uncovered" && (len(c.Paths) > 0 || c.PathsTotal > 0):
		return reason, fmt.Errorf("commandtext: reason %q lists no paths, but has %d of a total of %d: only a runner-uncovered check lists them", c.Reason, len(c.Paths), c.PathsTotal)
	case len(c.Paths) > maxListed:
		return reason, fmt.Errorf("commandtext: %d paths, at most %d", len(c.Paths), maxListed)
	case c.PathsTotal < len(c.Paths):
		return reason, fmt.Errorf("commandtext: a total of %d is below the %d paths listed", c.PathsTotal, len(c.Paths))
	case len(c.Paths) == 0 && c.PathsTotal != 0:
		return reason, fmt.Errorf("commandtext: a total of %d paths and none listed", c.PathsTotal)
	}
	return reason, nil
}

// checkCheckReference reports a reference or a version that does not fit the reason of c.
func checkCheckReference(c Check) error {
	if (c.Proposal == 0) != (c.Revision == 0) || c.Version < 0 {
		return fmt.Errorf("commandtext: check reference P%d-R%d and version %d do not fit", c.Proposal, c.Revision, c.Version)
	}
	beforeCanonical := c.Reason == "bootstrap-ready" || c.Reason == "bootstrap-awaiting" || c.Reason == "bootstrap-skipped"
	if (beforeCanonical && c.Version != 0) || (c.Reason == "bootstrap-ready" && c.Proposal == 0) {
		return fmt.Errorf("commandtext: reason %q does not fit reference P%d-R%d and version V%d: it has no canonical version, and a ready bootstrap has a reference", c.Reason, c.Proposal, c.Revision, c.Version)
	}
	if c.Reason == "approval-missing" && c.Proposal == 0 {
		return errors.New("commandtext: approval is missing for no revision")
	}
	if c.ApproveMode && (c.Reason != "approval-missing" || c.Version == 0) {
		return fmt.Errorf("commandtext: reason %q with version V%d has no approve with a mode: only an approval that is missing against a canonical has one", c.Reason, c.Version)
	}
	if c.Changes != nil {
		if c.Proposal == 0 {
			return errors.New("commandtext: the changes of no revision")
		}
		if err := validateChanges(*c.Changes); err != nil {
			return err
		}
	}
	if c.Proposal != 0 {
		if _, err := reference(c.Proposal, c.Revision); err != nil {
			return err
		}
	}
	if c.Version != 0 {
		return checkID("version", c.Version)
	}
	return nil
}

var checkStateName = map[CheckState]string{CheckSuccess: "success", CheckInProgress: "in_progress", CheckFailure: "failure", CheckNeutral: "neutral"}

// ExecutionText states the counts of a complete canonical run in the words of the results comment, which the Contract check summary says too.
func ExecutionText(e CheckExecution) string { return executionText(e) }
