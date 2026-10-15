package commandtext

import (
	"fmt"
	"strconv"
	"strings"
)

// MergeKind is the outcome of a merged pull request that gets a comment.
type MergeKind uint8

const (
	MergePromoted MergeKind = iota + 1
	MergeCorrected
	MergeBlocked
)

// Merge is what the outcome comment of a merged pull request states (ADR 0014, D-INTEGRATION). Version is V<n>:
// the new version of a promotion or a correction, or the canonical version a blocked merge leaves in force.
// Corrects (V<n>) is set exactly for MergeCorrected, Reason exactly for MergeBlocked. Source is the merge commit,
// 40 lowercase hex; it is empty only for the reason integration-unreadable, whose commit may never have been
// reported. Proposal and Revision name the contract change revision and are both zero when the merge had none.
type Merge struct {
	Kind               MergeKind
	Reason             string
	Source             string
	Version            int
	Corrects           int
	Proposal, Revision int
	Tier               Tier
}

// blockedReason is the plain-language account of one blocked reason. The text never quotes the repository, the
// pull request or a comment, so a comment is fixed text and numbers and stays far below GitHub's limit.
type blockedReason struct {
	text string
	next string // leads the recovery when the reason has its own
}

var blockedReasons = map[string]blockedReason{
	"unapproved-change": {text: "This pull request changed the protected files, but SuiteWard never saw a contract change proposal for it, so no owner approval exists for what was merged."},
	"invalid-source": {text: "The protected source or the declaration (`.suiteward.yml`) of the merge commit is invalid, for example an invalid declaration or a symlink or " +
		"submodule inside the protected scope, so SuiteWard cannot read its protected files."},
	"assessment-conflict":       {text: "SuiteWard already holds a different integrity assessment for this commit, so it cannot assess this merge again."},
	"approval-missing":          {text: "The owner's approval of this exact revision was not current when the pull request was merged."},
	"differs-from-approved":     {text: "The protected files at the merge commit differ from the exact revision the owner approved."},
	"not-current":               {text: "The approved revision was no longer the current revision of this contract change when it was merged."},
	"canonical-changed":         {text: "The canonical contract changed after the owner approved this revision, so the approval no longer applies to what was merged."},
	"policy-changed":            {text: "The owner's policy changed after the approval, so the approval no longer applies to what was merged."},
	"integrity-not-passed":      {text: "The integrity assessment of the merge commit did not pass."},
	"integration-mismatch":      {text: "The integration of this merge does not match the approved change."},
	"correction-context-reused": {text: "This restoration of an earlier version was already used for another correction, so it cannot be used again."},
	"fork-unsupported":          {text: "Pull requests from forks are not supported yet, so SuiteWard never promotes a contract change merged from one."},
	"shared-merge-commit": {text: "SuiteWard cannot tell which pull request the merge commit belongs to, because another merged pull request has the same merge commit. " +
		"An outcome already recorded for the other pull request stands."},
	"integration-unreadable": {text: "SuiteWard could not read the merge commit from GitHub within 30 minutes of seeing the pull request leave the open list, " +
		"or GitHub reported no merge commit for it."},
	"integrity-failed": {text: "SuiteWard's stored canonical contract failed its integrity verification, so SuiteWard refuses to promote anything on top of it.",
		next: "Next: the operator must restore the artifact store from a backup or re-bootstrap the repository. A pull request cannot fix this."},
}

// MergeComment renders the outcome comment of a merged pull request: for a promotion or a correction the new
// version and the merge commit, for a blocked merge that the canonical stays in force, why, and the recovery. It
// never says that tests ran. The values come from storage, so one that does not fit is an impossible state and panics.
func MergeComment(m Merge) string {
	tier := tierName(m.Tier)
	// Only a blocked merge can leave no canonical in force; integrity-failed needs one to have failed.
	noCanonical := m.Version == 0 && m.Kind == MergeBlocked && m.Reason != "integrity-failed"
	if (m.Version < 1 && !noCanonical) || (m.Proposal == 0) != (m.Revision == 0) || m.Proposal < 0 || m.Revision < 0 {
		panic(fmt.Sprintf("commandtext: merge version %d and reference P%d-R%d do not fit", m.Version, m.Proposal, m.Revision))
	}
	if !noCanonical {
		checkID("version", m.Version)
	}
	reason, blocked := blockedReasons[m.Reason]
	switch {
	case m.Kind != MergeBlocked && m.Kind != MergePromoted && m.Kind != MergeCorrected:
		panic(fmt.Sprintf("commandtext: unknown merge kind %d", m.Kind))
	case blocked != (m.Kind == MergeBlocked) || (m.Kind != MergeBlocked && m.Reason != ""):
		panic(fmt.Sprintf("commandtext: merge kind %d does not fit the reason %q", m.Kind, m.Reason))
	case (m.Kind == MergeCorrected) != (m.Corrects != 0) || m.Corrects < 0:
		panic(fmt.Sprintf("commandtext: merge kind %d does not fit the corrected version %d", m.Kind, m.Corrects))
	case m.Source == "" && m.Reason != "integration-unreadable":
		panic("commandtext: a merge names its merge commit unless it was unreadable")
	case m.Source != "" && !shaPattern.MatchString(m.Source):
		panic(fmt.Sprintf("commandtext: merge commit %q is not a 40-hex commit SHA", m.Source))
	}
	version := "V" + strconv.Itoa(m.Version)
	firstVersion := m.Kind == MergePromoted && m.Version == 1 // only a bootstrap promotes V1
	var facts []string
	if m.Source != "" {
		facts = append(facts, "- Merge commit: `"+m.Source+"`")
	}
	if m.Proposal != 0 {
		facts = append(facts, "- Proposal revision: "+Reference(m.Proposal, m.Revision))
	}
	facts = append(facts, "- Installation tier: `"+tier+"`")
	var lead, body string
	switch m.Kind {
	case MergePromoted:
		lead = "**SuiteWard promoted the canonical contract to " + version + "**"
		body = "This merge integrated the contract change the owner approved, and SuiteWard promoted " + version + " from the merge commit."
		if firstVersion {
			body = "This merge integrated the bootstrap baseline the owner approved, and SuiteWard promoted " + version + " from the merge commit. " +
				"This is the first canonical version."
		}
	case MergeCorrected:
		checkID("corrected version", m.Corrects)
		lead = "**SuiteWard promoted the canonical contract to " + version + " as a correction**"
		body = "This merge restored the protected files of an earlier version, and SuiteWard promoted " + version + " as a correction of V" + strconv.Itoa(m.Corrects) +
			". The history is kept."
	default:
		lead = "**SuiteWard did not promote this merge: the canonical contract stays at " + version + "**"
		if noCanonical {
			lead = "**SuiteWard did not promote this merge: SuiteWard has no canonical contract yet**"
		}
		body = reason.text
	}
	parts := []string{lead, body, strings.Join(facts, "\n")}
	if m.Tier == CoLocated {
		parts = append(parts, coLocatedNotice)
	}
	if m.Kind != MergeBlocked {
		parts = append(parts, "SuiteWard checked the approval and the integrity of the protected files at that commit. It did not run the tests.")
		return paragraphs(parts...)
	}
	if noCanonical {
		return paragraphs(append(parts, "To recover, the default branch now holds the merged files, so SuiteWard proposes the default branch's contract for approval on an open pull request. "+
			"Approve that proposal to make it the first canonical version.")...)
	}
	recovery := "open a new pull request that restores the protected files of " + version + ", or one that carries the integrated files for a new exact approval. There is no reset command."
	if reason.next != "" {
		parts = append(parts, reason.next+" After that, to recover, "+recovery)
	} else {
		parts = append(parts, "To recover, "+recovery)
	}
	return paragraphs(parts...)
}
