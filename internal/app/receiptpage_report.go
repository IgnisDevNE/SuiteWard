package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The change report of the current revision on the pull request page (docs/contracts/runtime.md, "The pull request page", item 8): the summary lines and a table of the
// changed files with no hunk, a link to the full report of the revision, which keeps the hunks, and a link to the report of each earlier revision of the proposal. The
// report is built here once, in the turn that the page holds, by the reports of the process, which bound what they read.

// reportSection is the change report of the revision. When the report cannot be built the section is one sentence and nothing else: no link, since the full report would
// answer 404 or 413 as well.
type reportSection struct {
	Show           bool
	Reference      string // P<n>-R<m>
	Sentence       string // why there is no report, empty when there is one
	Facts          []string
	Files          table
	More           string
	Full           pageLink // the report of the revision, with its hunks
	EarlierHeading string   // how many earlier revisions there are, and how many are listed when not all
	Earlier        []pageLink
	EarlierMore    string
}

// reportOf builds the section of the revision current of the proposal that the pull request carries, whose revisions are revisions, oldest first. Only the end of the
// context fails the page: a revision whose report cannot be built (no stored manifest, a file missing from the store, more bytes than a report is built from) says so in one
// sentence, and the cause is logged.
func reportOf(ctx context.Context, deps pageDeps, repo string, current contract.ProposalReference, revisions []contract.ProposalReference) (reportSection, error) {
	reference := referenceText(current)
	s := reportSection{Show: true, Reference: reference}
	built, err := deps.reports.build(ctx, current)
	if cause := ctx.Err(); cause != nil {
		return reportSection{}, cause
	}
	switch {
	case errors.Is(err, governance.ErrNotFound):
		s.Sentence = "There is no change report for this revision: SuiteWard has no stored files of it, or it has no earlier contract to compare with."
	case errors.Is(err, errReportTooLarge):
		s.Sentence = "This revision changes more than SuiteWard builds a change report from, so the report is not shown."
	case err != nil:
		deps.logger.WarnContext(ctx, "pull request page: the change report is unavailable", "reference", reference, "error", err)
		s.Sentence = "SuiteWard could not read the files of this revision, so the change report is not shown."
	}
	if s.Sentence != "" {
		return s, nil
	}
	s.Full = pageLink{Href: reportHref(repo, reference), Text: "The full change report of " + reference + ", with the changed lines"}
	s.Facts = commandtext.ChangesOf(built.Report).Facts(false)
	rows := make([][]string, len(built.Report.Files))
	for i, f := range built.Report.Files {
		rows[i] = fileRowCells(f)
	}
	var hidden int
	if s.Files, hidden, err = tableOf([]string{"Status", "File", "Lines"}, rows, fileTableBudget); err != nil {
		return reportSection{}, err
	}
	s.More = rowsNotShown(hidden, "file", "files")
	i := slices.IndexFunc(revisions, func(r contract.ProposalReference) bool { return r.RevisionID == current.RevisionID })
	if i < 0 { // the current revision is one of the revisions of its proposal
		return reportSection{}, fmt.Errorf("the revision %s is not among the revisions of its proposal", reference)
	}
	var earlier []pageLink // the revisions before the current one, the newest first
	for _, r := range slices.Backward(revisions[:i]) {
		text := referenceText(r)
		earlier = append(earlier, pageLink{Href: reportHref(repo, text), Text: text})
	}
	over := max(len(earlier)-maxEarlierLinks, 0)
	if s.Earlier, hidden, err = linkList(earlier[:len(earlier)-over], listBudget); err != nil {
		return reportSection{}, err
	}
	s.EarlierHeading = countedHeading("Earlier revisions of this proposal", len(earlier))
	if over > 0 {
		s.EarlierHeading += " (" + strconv.Itoa(maxEarlierLinks) + " listed)"
	}
	s.EarlierMore = entriesNotShown(hidden + over)
	return s, nil
}
