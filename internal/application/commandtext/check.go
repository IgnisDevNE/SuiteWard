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
	"bootstrap-pending": {CheckInProgress, "Waiting for the first baseline",
		"This repository has no approved baseline yet, so the contract of this head has nothing to be compared with.",
		"Next: the owner approves the baseline proposal in SuiteWard's bootstrap comment on the lowest-numbered open pull request; " +
			"SuiteWard proposes none while the default branch has no protected files."},
	"unchanged": {CheckSuccess, "No protected change",
		"The protected files of this head match the canonical contract.",
		"Next: nothing is required."},
	"ready": {CheckSuccess, "Ready for integration",
		"The owner approved the exact revision of this contract change against the current canonical contract, and the integrity " +
			"assessment of this head passed. It is ready for integration.",
		"Next: integrate this pull request when you are ready."},
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
	"fork-unsupported": {CheckInProgress, "Pull requests from forks are not supported",
		"Pull requests from forks are not supported yet, so SuiteWard does not assess this head and never proposes a contract change for it.",
		"Next: open the pull request from a branch of this repository."},
	"invalid-source": {CheckFailure, "Invalid contract source",
		"The contract declaration or the protected source of this head is invalid, for example an invalid `.suiteward.yml` " +
			"or a symlink or submodule inside the protected scope. The canonical contract stays in force.",
		"Next: fix `.suiteward.yml`, or remove the symlink or submodule from the protected scope, and push."},
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
	parts := []string{reason.text, strings.Join(facts, "\n")}
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
