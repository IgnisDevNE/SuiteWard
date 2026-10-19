package commandtext

import (
	"fmt"
	"strings"
)

// CheckState is the conclusion of the SuiteWard / Contract check (D-CHECKS).
type CheckState uint8

const (
	CheckSuccess CheckState = iota + 1
	CheckInProgress
	CheckFailure
)

// Check is what the output of a check run states. Reason is the stable code the check job stored, and State must
// be the one the code belongs to. Proposal and Revision name the revision the check concerns and are both zero
// when there is none; approval-missing requires them. Version (V<n>) is the canonical version, zero when there is
// none.
type Check struct {
	State              CheckState
	Reason             string
	Proposal, Revision int
	Version            int
	Tier               Tier
	// Paths are at most 20 of the PathsTotal runner files of a runner-uncovered check, each already cut by the caller; the other reasons have none.
	Paths      []string
	PathsTotal int
}

// checkReason is the output of one reason code: its state, a title, what happened and the next action. The text
// never quotes the repository, the pull request or a comment, so a summary is fixed text and numbers and stays
// far below GitHub's limit of 65,535 characters.
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
}

// CheckSummary renders the output title and summary of a check run: the state and what it means, the revision
// and the canonical version when there are any, the installation tier and the next action. It never says that
// tests ran. The reason, the state and the tier come from storage, so a value that does not fit is an impossible
// state and panics.
func CheckSummary(c Check) (title, summary string) {
	reason, known := checkReasons[c.Reason]
	if !known || reason.state != c.State {
		panic(fmt.Sprintf("commandtext: check reason %q does not fit state %d", c.Reason, c.State))
	}
	tier := tierName(c.Tier)
	if (c.Proposal == 0) != (c.Revision == 0) || c.Version < 0 {
		panic(fmt.Sprintf("commandtext: check reference P%d-R%d and version %d do not fit", c.Proposal, c.Revision, c.Version))
	}
	beforeCanonical := c.Reason == "bootstrap-ready" || c.Reason == "bootstrap-awaiting" || c.Reason == "bootstrap-skipped"
	if (beforeCanonical && c.Version != 0) || (c.Reason == "bootstrap-ready" && c.Proposal == 0) {
		panic(fmt.Sprintf("commandtext: reason %q does not fit reference P%d-R%d and version V%d: it has no canonical version, and a ready bootstrap has a reference", c.Reason, c.Proposal, c.Revision, c.Version))
	}
	if (c.Reason != "runner-uncovered" && (len(c.Paths) > 0 || c.PathsTotal > 0)) || len(c.Paths) > maxListed || c.PathsTotal < len(c.Paths) || (len(c.Paths) == 0 && c.PathsTotal != 0) {
		panic(fmt.Sprintf("commandtext: reason %q does not fit %d paths of a total of %d: only a runner-uncovered check lists at most %d", c.Reason, len(c.Paths), c.PathsTotal, maxListed))
	}
	facts := []string{"- State: `" + checkStateName[c.State] + "`"}
	var ref string
	if c.Proposal != 0 {
		ref = Reference(c.Proposal, c.Revision)
		facts = append(facts, "- Proposal revision: "+ref)
	}
	if c.Version != 0 {
		checkID("version", c.Version)
		facts = append(facts, fmt.Sprintf("- Canonical version: V%d", c.Version))
	}
	facts = append(facts, "- Installation tier: `"+tier+"`")
	text := reason.text
	if c.Reason == "approval-missing" && c.Version == 0 { // no canonical: the head proposes the first contract, it changes none
		text = "This pull request carries the proposed first contract " + ref + ", awaiting the owner's approval of that exact revision."
	}
	parts := []string{text}
	if len(c.Paths) > 0 {
		parts = append(parts, listingAndMore("Runner files that this head adds or changes outside the protected scope", c.Paths, c.PathsTotal))
	}
	parts = append(parts, strings.Join(facts, "\n"))
	if c.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	parts = append(parts, reason.next)
	if c.Reason == "approval-missing" {
		if ref == "" {
			panic("commandtext: approval is missing for no revision")
		}
		parts = append(parts, copyable("/suiteward approve "+ref+" <code>"))
	}
	parts = append(parts, "This check reports on the protected contract only. It does not run tests.")
	return reason.title, paragraphs(parts...)
}

var checkStateName = map[CheckState]string{CheckSuccess: "success", CheckInProgress: "in_progress", CheckFailure: "failure"}
