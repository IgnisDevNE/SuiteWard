package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const cmtReportBase = "http://127.0.0.1:8080"

// cmtRevisionText is the comment of cmtNotice for the isolated tier: with the numbers of changes and the link url, or as it was when it has neither.
func cmtRevisionText(changes *commandtext.Changes, url string, unavailable bool) string {
	notice := cmtNotice()
	return commandtext.RevisionComment(commandtext.Revision{Proposal: 2, Revision: 2, Previous: "P2-R1", Canonical: 1, Added: notice.Added, Modified: notice.Modified, Removed: notice.Removed,
		AddedTotal: 1, ModifiedTotal: 2, RemovedTotal: 1, Tier: commandtext.Isolated, Changes: changes, ReportURL: url, ChangesUnavailable: unavailable})
}

// bogusReport is a report that ChangesOf turns into numbers the renderer refuses: a file that counts for no status.
func bogusReport() revisionReport {
	return revisionReport{Base: "V1", Report: report.Report{Files: []report.File{{Path: "x", Status: "bogus", CountsKnown: true, LinesAdded: 1}}}}
}

func TestCommentPublisherStatesTheNumbersOfTheRevisionAndLinksItsReport(t *testing.T) {
	w := newCmtWorld(t)
	w.reports.err, w.reports.report = nil, revisionReport{Base: "V1", Report: smallReport(t)}
	w.pub.reportBase = cmtReportBase
	changes := commandtext.ChangesOf(smallReport(t))

	w.mustDeliver(cmtRevisionMessage(t, cmtNotice(), "isolated"))

	w.wantPost(cmtRevisionKey, cmtRevisionText(&changes, cmtReportBase+"/reports/acme/widgets/P2-R2", false))
	want := contract.ProposalReference{ProjectID: owner.ProjectID(cmtRepoID), SuiteID: "default", ProposalID: "P2", RevisionID: "R2"}
	if len(w.reports.asked) != 1 || w.reports.asked[0] != want {
		t.Fatalf("the report of %v was asked, want of %v", w.reports.asked, want)
	}
}

func TestCommentPublisherStatesTheNumbersWithoutALinkWhenNoBaseURLIsSet(t *testing.T) {
	w := newCmtWorld(t)
	w.reports.err, w.reports.report = nil, revisionReport{Base: "V1", Report: smallReport(t)}
	changes := commandtext.ChangesOf(smallReport(t))

	w.mustDeliver(cmtRevisionMessage(t, cmtNotice(), "isolated"))

	w.wantPost(cmtRevisionKey, cmtRevisionText(&changes, "", false))
}

func TestCommentPublisherPostsTheCommentWithoutNumbersWhenTheReportCannotBeBuilt(t *testing.T) {
	for name, err := range map[string]error{
		"the manifest was never stored": governance.ErrNotFound, "a file is missing": errors.New("read the file: not exist"),
		"the revision is too large": errReportTooLarge, "the store failed": errors.New("store down"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.reports.err = err
			w.pub.reportBase = cmtReportBase

			w.mustDeliver(cmtRevisionMessage(t, cmtNotice(), "isolated"))

			w.wantPost(cmtRevisionKey, cmtRevisionText(nil, "", true))
			if !strings.Contains(w.logs.String(), "change report") {
				t.Errorf("the missing report is not logged: %q", w.logs.String())
			}
		})
	}
}

// The comment carries the command that approves the revision: whatever goes wrong with the numbers, it is still posted as it was.
func TestCommentPublisherPostsTheOldCommentWhenTheNumbersCannotBeRendered(t *testing.T) {
	for name, tc := range map[string]struct {
		report revisionReport
		base   string
	}{
		"numbers the renderer refuses": {report: bogusReport(), base: cmtReportBase},
		"a base URL it refuses":        {report: revisionReport{Base: "V1", Report: smallReport(t)}, base: "https://evil.example"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.reports.err, w.reports.report = nil, tc.report
			w.pub.reportBase = tc.base

			w.mustDeliver(cmtRevisionMessage(t, cmtNotice(), "isolated"))

			w.wantPost(cmtRevisionKey, cmtRevisionText(nil, "", false))
			if !strings.Contains(w.logs.String(), "without the numbers") {
				t.Errorf("the dropped numbers are not logged: %q", w.logs.String())
			}
		})
	}
}

type cancellingReports struct{ cancel context.CancelFunc }

func (c cancellingReports) revisionReport(ctx context.Context, _ contract.ProposalReference) (revisionReport, error) {
	c.cancel() // the delivery is cancelled while the report is built
	return revisionReport{}, ctx.Err()
}

// A cancelled delivery is not a missing report: the relay delivers the comment again, and nothing is posted with numbers that were never counted.
func TestCommentPublisherPostsNothingWhenTheDeliveryIsCancelledWhileTheReportIsBuilt(t *testing.T) {
	w := newCmtWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	w.pub.reports = cancellingReports{cancel}
	message := cmtRevisionMessage(t, cmtNotice(), "isolated")

	err := w.pub.publish(ctx, message.Key, message.Payload)

	if !errors.Is(err, context.Canceled) || len(w.writer.posts()) != 0 {
		t.Fatalf("publish = %v with %d posts, want context.Canceled and none", err, len(w.writer.posts()))
	}
}

func TestOtherCommentsDoNotAskForAReport(t *testing.T) {
	w := newCmtWorld(t)
	w.mustDeliver(cmtMust(newNoticeMessage(cmtTarget())))
	if len(w.reports.asked) != 0 {
		t.Fatalf("the report of %v was asked for a comment that is not about a revision", w.reports.asked)
	}
}
