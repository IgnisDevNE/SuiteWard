package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func (w *chkWorld) posted(t *testing.T) string {
	t.Helper()
	posts := w.runner.posts()
	if len(posts) != 1 {
		t.Fatalf("posted %d check runs, want 1", len(posts))
	}
	return posts[0].Summary
}

func TestCheckPublisherStatesTheNumbersOfTheRevisionWithoutALinkOrAPath(t *testing.T) {
	w := newChkWorld()
	w.reports.err, w.reports.report = nil, revisionReport{Base: "V2", Report: smallReport(t)}

	if err := w.deliver(chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")); err != nil {
		t.Fatal(err)
	}

	summary := w.posted(t)
	for _, want := range []string{"- Files: 1 added, 1 modified, 0 removed\n", "- Lines: 5 added, 1 removed (counted in 2 of 2 files)\n", "- Marker lines seen: 1 removed assertion line, 1 added skip line\n"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "http") || strings.Contains(summary, "tests/a_test.go") {
		t.Errorf("a public check summary links the report or names a path:\n%s", summary)
	}
	want := contract.ProposalReference{ProjectID: owner.ProjectID(cmtRepoID), SuiteID: "default", ProposalID: "P1", RevisionID: "R2"}
	if len(w.reports.asked) != 1 || w.reports.asked[0] != want {
		t.Fatalf("the report of %v was asked, want of %v", w.reports.asked, want)
	}
}

func TestCheckPublisherAsksForNoReportWhereThereIsNoRevisionToCompare(t *testing.T) {
	for name, m := range map[string]governance.OutboxMessage{
		"a check without a revision":          chkMessage(t, checks.Success, "unchanged", ""),
		"a bootstrap, which has no canonical": chkMessage(t, checks.Success, "bootstrap-ready", "P1-R1"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newChkWorld()
			w.reports.err, w.reports.report = nil, revisionReport{Base: "V2", Report: smallReport(t)}
			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}
			if len(w.reports.asked) != 0 || strings.Contains(w.posted(t), "Lines:") {
				t.Fatalf("the report of %v was asked, or the summary states lines:\n%s", w.reports.asked, w.posted(t))
			}
		})
	}
}

func TestCheckPublisherAsksForNoReportOfAMessageItDrops(t *testing.T) {
	w := newChkWorld()
	w.reports.err, w.reports.report = nil, revisionReport{Base: "V2", Report: smallReport(t)}
	w.ledger.record.Generation = 4 // a newer state superseded generation 3

	if err := w.deliver(chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")); err != nil {
		t.Fatal(err)
	}

	if len(w.reports.asked) != 0 || len(w.runner.posts()) != 0 {
		t.Fatalf("asked %v and posted %d runs, want neither for a stale message", w.reports.asked, len(w.runner.posts()))
	}
}

// The check is the gate of the merge: whatever goes wrong with the numbers, it is still posted without them.
func TestCheckPublisherPostsTheCheckWithoutNumbersWhenTheReportCannotBeUsed(t *testing.T) {
	plain := newChkWorld()
	if err := plain.deliver(chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")); err != nil {
		t.Fatal(err)
	}
	want := plain.posted(t)
	for name, tc := range map[string]struct {
		err    error
		report revisionReport
	}{
		"the manifest was never stored": {err: governance.ErrNotFound},
		"a file is missing":             {err: errors.New("read the file: not exist")},
		"the revision is too large":     {err: errReportTooLarge},
		"numbers the renderer refuses":  {report: bogusReport()},
	} {
		t.Run(name, func(t *testing.T) {
			w := newChkWorld()
			w.reports.err, w.reports.report = tc.err, tc.report

			if err := w.deliver(chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")); err != nil {
				t.Fatal(err)
			}

			if got := w.posted(t); got != want {
				t.Fatalf("summary:\n%s\nwant the one without numbers:\n%s", got, want)
			}
			if !strings.Contains(w.logs.String(), "change report") {
				t.Errorf("the missing numbers are not logged: %q", w.logs.String())
			}
		})
	}
}

func TestCheckPublisherPostsNothingWhenTheDeliveryIsCancelledWhileTheReportIsBuilt(t *testing.T) {
	w := newChkWorld()
	ctx, cancel := context.WithCancel(t.Context())
	w.pub.reports = cancellingReports{cancel}
	m := chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")

	err := w.pub.publish(ctx, m.Key, m.Payload)

	if !errors.Is(err, context.Canceled) || len(w.runner.posts()) != 0 {
		t.Fatalf("publish = %v with %d runs posted, want context.Canceled and none", err, len(w.runner.posts()))
	}
}
