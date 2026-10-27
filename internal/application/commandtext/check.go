package commandtext

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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
	// owner's unblock of an unavailable one. The reasons canonical-pending, canonical-unavailable and canonical-failing mark it too.
	Execution *CheckExecution
	Unblocked bool
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
	"bootstrap-awaiting": {CheckInProgress, "No canonical contract yet",
		"SuiteWard has no canonical contract yet. This head is not assessed against a contract until the first one exists.",
		"Next: the owner comments `/suiteward propose` on the pull request that should carry the first contract, " +
			"or `/suiteward skip <code>` with a current code from the authenticator app to let this pull request merge before the first contract."},
	"bootstrap-skipped": {CheckSuccess, "Skipped before the first contract",
		"Skipped by the owner before the first contract. No canonical contract exists and nothing was assessed on this head: this is not a contract assessment.",
		"Next: nothing is required. Once the first contract exists, later pull requests are assessed against it."},
	"unchanged": {CheckSuccess, "No protected change",
		"The protected files of this head match the canonical contract.",
		"Next: nothing is required."},
	"ready": {CheckSuccess, "Ready for integration",
		"The owner approved the exact revision of this contract change against the current canonical contract, and the integrity " +
			"assessment of this head passed. It is ready for integration.",
		"Next: integrate this pull request when you are ready."},
	"bootstrap-ready": {CheckSuccess, "Bootstrap ready for integration",
		"The owner approved the exact revision of the first baseline proposed from this pull request, and SuiteWard assessed the integrity of this head. " +
			"No canonical contract exists yet. SuiteWard will promote the first canonical version from the exact merge commit after the merge.",
		"Next: integrate this pull request when you are ready. A later push that changes the protected files needs a new approval."},
	"approval-missing": {CheckInProgress, "Awaiting owner approval",
		"This head changes the protected contract and has no current approval from the owner for its exact revision.",
		"To approve this exact revision, comment:"},
	"not-current": {CheckInProgress, "Contract change is not current",
		"The contract change for this head is not current against the canonical contract. SuiteWard evaluates it again on its next run.",
		"Next: nothing is required. If this stays in progress, update the branch from its base so SuiteWard can assess the current contract."},
	"assessment-pending": {CheckInProgress, "Assessment pending",
		"SuiteWard has not finished assessing this head, for example because GitHub could not be read yet.",
		"Next: nothing is required; SuiteWard tries again on its next run."},
	"source-unavailable": {CheckInProgress, "Source unavailable",
		"GitHub truncated the file tree of this head, so SuiteWard cannot read the protected files in full.",
		"Next: SuiteWard cannot check this head until GitHub returns the full tree; reducing the number of files in the repository may help."},
	"runner-uncovered": {CheckInProgress, "Runner files outside the protected scope",
		"This pull request adds or changes runner files that the protected scope does not cover. A runner file can change how the protected tests run without touching any of them, " +
			"so this head is not ready until the owner decides.",
		"Next: protect the files by committing the suggested declaration, which adds their runner patterns, and approving the new revision, " +
			"or ignore them by adding the paths to `exclude` and approving that revision. SuiteWard gives both declarations in a comment on this pull request."},
	"fork-unsupported": {CheckInProgress, "Pull requests from forks are not supported",
		"Pull requests from forks are not supported yet, so SuiteWard does not assess this head and never proposes a contract change for it.",
		"Next: open the pull request from a branch of this repository."},
	"invalid-source": {CheckFailure, "Invalid contract source",
		"The contract declaration or the protected source of this head is invalid, for example an invalid `.suiteward.yml` " +
			"or a symlink that the protected scope reaches. The canonical contract stays in force.",
		"Next: fix `.suiteward.yml`, or remove the symlink or point it to a regular file, or accept the symlink by adding its exact anchored path to `exclude`, a scope reduction that the owner approves and that leaves nothing reached through it protected, and push."},
	"integrity-failed": {CheckFailure, "Canonical contract failed its integrity check",
		"SuiteWard's stored canonical contract failed its integrity verification, so SuiteWard cannot trust it and blocks every pull request of this repository until it is restored.",
		"Next: the operator must restore the artifact store from a backup or re-bootstrap the repository. Pushing to this pull request does not help."},
	"owner-command-rejected": {CheckFailure, "Owner command rejected",
		"The owner's latest command on this pull request was rejected; SuiteWard's reply to it says why.",
		"Next: comment a corrected command with a new code, or push a new head."},
	// The reasons of the canonical gate under canonical: required (docs/contracts/runtime.md, "The canonical gate"). Their summaries state counts and no repository text.
	"canonical-pending": {CheckInProgress, "Canonical run pending",
		"The run of the approved tests on this head has not finished: it is waiting for an executor, or it is running. The check does not pass until the run ends.",
		"Next: nothing is required; this check changes when the run ends."},
	"canonical-unavailable": {CheckInProgress, "Canonical run unavailable",
		"SuiteWard could not run the approved tests on this head, so the run is unavailable and the check does not pass.",
		"Next: push a new head to run the approved tests again. To let this head pass without that run, the owner comments:"},
	"canonical-failing": {CheckFailure, "Canonical tests failing",
		"At least one approved test fails, is missing or is flaky on this head.",
		"Next: fix the code under test, or push a head on which the approved tests pass."},
}

// canonicalReasons are the reasons of the canonical gate. They, the counts of a run and the owner's unblock mark the summary of canonical: required.
var canonicalReasons = []string{"canonical-pending", "canonical-unavailable", "canonical-failing"}

// unblockedText is what the summary says of a head whose unavailable canonical run the owner unblocked.
const unblockedText = "The owner unblocked this head: its canonical run was unavailable, so the approved tests did not run on it."

// executionText states the counts of a complete canonical run, and only the counts: no test name, no path. A run with no declared test ran no test.
func executionText(e CheckExecution) string {
	ran := counted(e.Tests, "test ran", "tests ran")
	line := "Canonical run on this head: " + ran + "."
	if e.Tests > 0 {
		line = fmt.Sprintf("Canonical run on this head: %s: %d passed, %d skipped, %d failed, %d missing, %d flaky.", ran, e.Passed, e.Skipped, e.Failed, e.Missing, e.Flaky)
	}
	line += fmt.Sprintf("\nExcluded tests (not run): %d.", e.Excluded)
	if e.Excluded > 0 {
		line += " Excluded tests are not covered."
	}
	return line
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
		facts = append(facts, fmt.Sprintf("- Canonical version: V%d", c.Version))
	}
	facts = append(facts, tierLine(c.Tier))
	if c.Changes != nil {
		facts = append(facts, changeFacts(*c.Changes, false)...)
	}
	text := reason.text
	if c.Reason == "approval-missing" && c.Version == 0 { // no canonical: the head proposes the first contract, it changes none
		text = "This pull request carries the proposed first contract " + ref + ", awaiting the owner's approval of that exact revision."
	}
	parts := []string{text}
	if len(c.Paths) > 0 {
		parts = append(parts, listingAndMore("Runner files that this head adds or changes outside the protected scope", c.Paths, c.PathsTotal))
	}
	parts = append(parts, strings.Join(facts, "\n"))
	if c.Changes != nil {
		parts = append(parts, "Marker lines are lines to look at, not a verdict.")
	}
	if c.Execution != nil {
		parts = append(parts, executionText(*c.Execution))
	}
	if c.Unblocked {
		parts = append(parts, unblockedText)
	}
	if c.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts, reason.next)
	switch c.Reason {
	case "approval-missing":
		parts = append(parts, codeCommand("approve", ref))
	case "canonical-unavailable":
		parts = append(parts, copyable("/suiteward unblock <code>"))
	}
	parts = append(parts, closingText(c))
	return reason.title, paragraphs(parts...)
}

// checkRequired reports why the counts, the unblock and the canonical reasons of c do not fit together: the counts of a complete run go with a success (which no
// test blocks) or with canonical-failing (which one does), the unblock with a success and nothing else, and a pending or unavailable run has no counts.
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
	case c.Execution != nil && !success && c.Reason != "canonical-failing":
		return fmt.Errorf("commandtext: reason %q has no counts of a canonical run", c.Reason)
	case c.Unblocked && (!success || c.Execution != nil):
		return fmt.Errorf("commandtext: reason %q does not fit an unblocked run, which is lifted only to a success and has no counts", c.Reason)
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
