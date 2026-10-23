package report_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

// expensive is a pair of files that costs the comparison millions of steps: an edit distance of exactly 1000 around a
// long run of one repeated line, which every diagonal the comparison tries then follows to its end.
func expensive() (old, proposed string) {
	run := strings.Repeat("x\n", 15_000)
	return strings.Repeat("y\n", 250) + run + strings.Repeat("y\n", 250), strings.Repeat("z\n", 250) + run + strings.Repeat("z\n", 250)
}

func TestBuildSaysWhichFilesTheSharedWorkBudgetStopped(t *testing.T) {
	t.Parallel()
	old, proposed := expensive()
	var changes []report.FileChange
	for i := range 20 {
		changes = append(changes, mod(fmt.Sprintf("e%02d.txt", i), old, proposed))
	}
	changes = append(changes,
		mod("zz.py", "assert x\nA\nB\n", "B\nA\nC\n"), // changes at both ends: it needs the comparison
		mod("zzz.py", "assert x\nkeep\n", "keep\n"),   // one side is empty once what is common is trimmed: it needs none
	)
	r := build(t, changes...)
	if !r.Truncated {
		t.Fatal("the report must say the work budget ran out")
	}
	if first := r.Files[0]; first.Limit != "" || !first.CountsKnown || first.LinesAdded != 500 || first.LinesRemoved != 500 {
		t.Fatalf("the first file is compared: got Limit=%q, +%d -%d", first.Limit, first.LinesAdded, first.LinesRemoved)
	}
	stopped := false
	for _, f := range r.Files[:20] {
		switch f.Limit {
		case "":
			if stopped {
				t.Errorf("%s was compared after the budget ran out", f.Path)
			}
		case report.LimitBudget:
			stopped = true
			if f.CountsKnown || len(f.Hunks) != 0 {
				t.Errorf("%s: a file the budget stopped has unknown counts and no hunks, got %+v", f.Path, f)
			}
		default:
			t.Errorf("%s: Limit = %q, want a file compared or stopped by the budget", f.Path, f.Limit)
		}
	}
	if !stopped {
		t.Fatal("twenty expensive files must run the budget out")
	}
	pair, cheap := r.Files[20], r.Files[21]
	if pair.Limit != report.LimitBudget {
		t.Errorf("zz.py: Limit = %q, want %q", pair.Limit, report.LimitBudget)
	}
	if want := []report.Marker{{Kind: report.MarkRemovedAssertion, Path: "zz.py", Line: 1, Text: "assert x"}}; !reflect.DeepEqual(pair.Markers, want) {
		t.Errorf("zz.py: a file the budget stopped still has its markers: got %+v, want %+v", pair.Markers, want)
	}
	if cheap.Limit != "" || !cheap.CountsKnown || len(cheap.Markers) != 1 {
		t.Errorf("zzz.py needs no comparison and is compared: got Limit=%q, markers %+v", cheap.Limit, cheap.Markers)
	}
}

func TestBuildMarksFilesTooDifferentToCompare(t *testing.T) {
	t.Parallel()
	// 601 lines each and none in common: more than 1000 edits, so no diff. The markers come from the lines of one side
	// that the other lacks.
	c := mod("x.py", differ("o", 600)+"assert x\n", differ("n", 600)+"pytest.skip('x')\n")
	got := one(t, c)
	want := report.File{
		Path: "x.py", Status: report.Modified, OldSize: len(c.Old), NewSize: len(c.New), Limit: report.LimitEditDistance,
		Markers: []report.Marker{
			{Kind: report.MarkRemovedAssertion, Path: "x.py", Line: 601, Text: "assert x"},
			{Kind: report.MarkAddedSkip, Path: "x.py", Line: 601, Text: "pytest.skip('x')"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if r := build(t, c); r.Truncated || r.Totals.Markers[report.MarkRemovedAssertion] != 1 || r.Totals.Markers[report.MarkAddedSkip] != 1 {
		t.Fatalf("totals = %+v, truncated = %v; a file too different to compare is not a truncated report", r.Totals, r.Truncated)
	}
	moved := mod("x.py", differ("o", 600)+"assert x\n", differ("n", 600)+"    assert x\n")
	if got := one(t, moved); got.Limit != report.LimitEditDistance || len(got.Markers) != 0 {
		t.Fatalf("a line that the other side has too, reindented, is no marker: got Limit=%q, %+v", got.Limit, got.Markers)
	}
}

func TestBuildModifiedFileOverTheSizeBoundHasNoMarkers(t *testing.T) {
	t.Parallel()
	got := one(t, mod("x.py", strings.Repeat("assert x\n", 20_001), "y\n"))
	if got.Limit != report.LimitSize || len(got.Markers) != 0 {
		t.Fatalf("got Limit=%q, %+v; a file over the size bound is not read line by line", got.Limit, got.Markers)
	}
}

func TestBuildSaysWhichFileLostMarkers(t *testing.T) {
	t.Parallel()
	r := build(t,
		removed("a.py", strings.Repeat("assert x\n", 1000)), // exactly what the report lists
		removed("b.py", "assert y\nassert z\n"),             // none of its markers fits
		removed("c.py", "x\n"),                              // none to lose
	)
	a, b, c := r.Files[0], r.Files[1], r.Files[2]
	if a.MarkersTruncated || len(a.Markers) != 1000 {
		t.Errorf("a.py: got MarkersTruncated=%v with %d markers, want all 1000", a.MarkersTruncated, len(a.Markers))
	}
	if !b.MarkersTruncated || len(b.Markers) != 0 {
		t.Errorf("b.py: got MarkersTruncated=%v with %d markers, want none and the flag", b.MarkersTruncated, len(b.Markers))
	}
	if c.MarkersTruncated {
		t.Error("c.py lost no marker")
	}
	if got := r.Totals.Markers[report.MarkRemovedAssertion]; got != 1002 {
		t.Errorf("marker count = %d, want the exact 1002", got)
	}
}
