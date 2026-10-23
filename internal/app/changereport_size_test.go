package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The size of a file is known before its bytes are held: a revision over the budget is refused before a byte of its files is read.
func TestARevisionReportRefusesBeforeItReadsWhatWouldPassItsBudget(t *testing.T) {
	w, _ := reportWorld(t)
	content := &watchedContent{content: w.content}
	reports := newChangeReports(w.suites, content)
	reports.budget = len("v1") + len("v2") - 1

	if _, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1")); !errors.Is(err, errReportTooLarge) {
		t.Fatalf("revisionReport = %v, want errReportTooLarge", err)
	}

	for _, d := range content.read {
		if d == artifact.Hash([]byte("v1")) || d == artifact.Hash([]byte("v2")) {
			t.Errorf("read %s, a file of a revision that is over the budget", d)
		}
	}
}

// A report does not compare a file over 1 MiB, so a side that big, and the other side of its file, are never read: their sizes are all the report has of them.
func TestARevisionReportReadsNoSideOverTheFileBoundOfTheReport(t *testing.T) {
	big := strings.Repeat("0123456789\n", 120_000) // 1.3 MB
	declaration := "version: 1\ninclude: [/tests/**]\n"
	w := newCJWorldOf(t, map[string]string{".suiteward.yml": declaration, "tests/a_test.go": "v1", "tests/big_test.go": big})
	w.tree(cjHead2, map[string]string{".suiteward.yml": declaration, "tests/a_test.go": big + "changed", "tests/new_big.txt": big})
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	content := &watchedContent{content: w.content}
	reports := newChangeReports(w.suites, content)
	reports.budget = 1 // the bytes of a side that is not read do not count

	got, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))

	if err != nil {
		t.Fatalf("revisionReport: %v", err)
	}
	var summary []string
	for _, f := range got.Report.Files {
		summary = append(summary, fmt.Sprintf("%s %s %s %d/%d counted=%v +%d/-%d %d markers", f.Status, f.Path, f.Limit, f.OldSize, f.NewSize, f.CountsKnown, f.LinesAdded, f.LinesRemoved, len(f.Markers)))
	}
	want := []string{
		fmt.Sprintf("modified tests/a_test.go too-large 2/%d counted=false +0/-0 0 markers", len(big)+len("changed")),
		fmt.Sprintf("removed tests/big_test.go too-large %d/0 counted=false +0/-0 1 markers", len(big)),
		fmt.Sprintf("added tests/new_big.txt too-large 0/%d counted=false +0/-0 0 markers", len(big)),
	}
	if strings.Join(summary, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files:\n%s\nwant:\n%s", strings.Join(summary, "\n"), strings.Join(want, "\n"))
	}
	if totals := got.Report.Totals; totals.Files[report.Modified] != 1 || totals.Files[report.Removed] != 1 || totals.Files[report.Added] != 1 || totals.Markers[report.MarkTestFileRemoved] != 1 || totals.LinesAdded != 0 || totals.LinesRemoved != 0 {
		t.Errorf("totals %+v, want one file of each status, the removal of a test file seen and no line counted", totals)
	}
	if len(content.read) != 1 || content.read[0] != manifestDigestOf(t, w, revisionRef("P2", "R1")) {
		t.Errorf("read %v, want the manifest of the revision and no file", content.read)
	}
}
