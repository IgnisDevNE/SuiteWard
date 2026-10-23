package report_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

func mod(path, old, proposed string) report.FileChange {
	return report.FileChange{Path: path, Status: report.Modified, Old: []byte(old), New: []byte(proposed)}
}

func added(path, content string) report.FileChange {
	return report.FileChange{Path: path, Status: report.Added, New: []byte(content)}
}

func removed(path, content string) report.FileChange {
	return report.FileChange{Path: path, Status: report.Removed, Old: []byte(content)}
}

// ls reads a hunk from lines written as the unified diff prefix and the text.
func ls(spec ...string) []report.Line {
	out := make([]report.Line, len(spec))
	for i, s := range spec {
		out[i] = report.Line{Kind: report.LineKind(s[0]), Text: s[1:]}
	}
	return out
}

// numbered is n lines "l1".."ln", the ones in changed replaced.
func numbered(n int, changed map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if s, ok := changed[i]; ok {
			b.WriteString(s)
		} else {
			fmt.Fprintf(&b, "l%d", i)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func build(t *testing.T, changes ...report.FileChange) report.Report {
	t.Helper()
	r, err := report.Build(changes)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

func one(t *testing.T, c report.FileChange) report.File {
	t.Helper()
	r := build(t, c)
	if len(r.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(r.Files))
	}
	return r.Files[0]
}

// counted is the File of c whose counts are exact.
func counted(c report.FileChange, plus, minus int, hunks ...report.Hunk) report.File {
	return report.File{
		Path: c.Path, Status: c.Status, OldSize: len(c.Old), NewSize: len(c.New),
		LinesAdded: plus, LinesRemoved: minus, CountsKnown: true, Hunks: hunks,
	}
}

func TestBuildFiles(t *testing.T) {
	t.Parallel()
	identical := mod("a.txt", "a\nb\n", "a\nb\n")
	bothEmpty := mod("a.txt", "", "")
	addedEmpty := added("a.txt", "")
	removedEmpty := removed("a.txt", "")
	addedFile := added("a.txt", "x\ny\n")
	removedFile := removed("a.txt", "x\ny\n")
	middle := mod("a.txt", numbered(10, nil), numbered(10, map[int]string{5: "five"}))
	first := mod("a.txt", numbered(10, nil), numbered(10, map[int]string{1: "one"}))
	last := mod("a.txt", numbered(10, nil), numbered(10, map[int]string{10: "ten"}))
	noNewline := mod("a.txt", "a\nb", "a\nb\n")
	crlf := mod("a.txt", "a\r\nb\r\n", "a\nb\n")
	fromEmpty := mod("a.txt", "", "x\n")
	toEmpty := mod("a.txt", "x\n", "")
	cases := []struct {
		name   string
		change report.FileChange
		want   report.File
	}{
		{"identical bytes", identical, counted(identical, 0, 0)},
		{"both empty", bothEmpty, counted(bothEmpty, 0, 0)},
		{"added empty file", addedEmpty, counted(addedEmpty, 0, 0)},
		{"removed empty file", removedEmpty, counted(removedEmpty, 0, 0)},
		{"added", addedFile, counted(addedFile, 2, 0, report.Hunk{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 2, Lines: ls("+x", "+y")})},
		{"removed", removedFile, counted(removedFile, 0, 2, report.Hunk{OldStart: 1, OldLines: 2, NewStart: 0, NewLines: 0, Lines: ls("-x", "-y")})},
		{"modified empty to content", fromEmpty, counted(fromEmpty, 1, 0, report.Hunk{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 1, Lines: ls("+x")})},
		{"modified content to empty", toEmpty, counted(toEmpty, 0, 1, report.Hunk{OldStart: 1, OldLines: 1, NewStart: 0, NewLines: 0, Lines: ls("-x")})},
		{"three lines of context", middle, counted(middle, 1, 1, report.Hunk{
			OldStart: 2, OldLines: 7, NewStart: 2, NewLines: 7,
			Lines: ls(" l2", " l3", " l4", "-l5", "+five", " l6", " l7", " l8"),
		})},
		{"change at the first line", first, counted(first, 1, 1, report.Hunk{
			OldStart: 1, OldLines: 4, NewStart: 1, NewLines: 4, Lines: ls("-l1", "+one", " l2", " l3", " l4"),
		})},
		{"change at the last line", last, counted(last, 1, 1, report.Hunk{
			OldStart: 7, OldLines: 4, NewStart: 7, NewLines: 4, Lines: ls(" l7", " l8", " l9", "-l10", "+ten"),
		})},
		{"no newline at the end is a change", noNewline, counted(noNewline, 1, 1, report.Hunk{
			OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2,
			Lines: []report.Line{
				{Kind: report.LineContext, Text: "a"},
				{Kind: report.LineRemoved, Text: "b", NoNewline: true},
				{Kind: report.LineAdded, Text: "b"},
			},
		})},
		{"CRLF to LF changes every line and keeps the CR", crlf, counted(crlf, 2, 2, report.Hunk{
			OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2, Lines: ls("-a\r", "-b\r", "+a", "+b"),
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := one(t, tc.change); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestBuildMergesHunksUpToSixUnchangedLines(t *testing.T) {
	t.Parallel()
	// Changes at lines 3 and 4+gap are separated by gap unchanged lines.
	const lines = 30
	changed := func(gap int) report.File {
		return one(t, mod("a.txt", numbered(lines, nil), numbered(lines, map[int]string{3: "x", 4 + gap: "y"})))
	}
	merged := changed(6)
	if len(merged.Hunks) != 1 || merged.Hunks[0].OldStart != 1 || merged.Hunks[0].OldLines != 13 {
		t.Fatalf("six unchanged lines between changes must give one hunk of lines 1..13, got %+v", merged.Hunks)
	}
	split := changed(7)
	if len(split.Hunks) != 2 {
		t.Fatalf("seven unchanged lines between changes must give two hunks, got %+v", split.Hunks)
	}
	if h := split.Hunks[0]; h.OldStart != 1 || h.OldLines != 6 {
		t.Fatalf("first hunk = %+v, want lines 1..6", h)
	}
	if h := split.Hunks[1]; h.OldStart != 8 || h.OldLines != 7 {
		t.Fatalf("second hunk = %+v, want lines 8..14", h)
	}
}

func TestBuildBinary(t *testing.T) {
	t.Parallel()
	for _, c := range []report.FileChange{
		mod("a.bin", "a\x00b", "a\n"),
		mod("a.bin", "a\n", "a\x00b"),
		added("a.bin", "\x00"),
		removed("a.bin", "\x00\x00"),
	} {
		want := report.File{Path: c.Path, Status: c.Status, OldSize: len(c.Old), NewSize: len(c.New), Binary: true}
		if got := one(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", c.Status, got, want)
		}
	}
}

func differ(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

func TestBuildTooLarge(t *testing.T) {
	t.Parallel()
	many := strings.Repeat("x\n", 20_001)
	fits := strings.Repeat("x\n", 20_000)
	wide := strings.Repeat("a", 1<<20+1)
	t.Run("a modified side over the bound has unknown counts", func(t *testing.T) {
		t.Parallel()
		for _, c := range []report.FileChange{mod("a.txt", many, "y\n"), mod("a.txt", "y\n", many), mod("a.txt", wide, "y\n"), mod("a.txt", "y\n", wide)} {
			want := report.File{Path: "a.txt", Status: report.Modified, OldSize: len(c.Old), NewSize: len(c.New), TooLarge: true}
			if got := one(t, c); !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		}
	})
	t.Run("an added or removed file over the bound keeps exact counts without hunks", func(t *testing.T) {
		t.Parallel()
		plus, minus := added("a.txt", many), removed("a.txt", many)
		wantPlus, wantMinus := counted(plus, 20_001, 0), counted(minus, 0, 20_001)
		wantPlus.TooLarge, wantMinus.TooLarge = true, true
		if got := one(t, plus); !reflect.DeepEqual(got, wantPlus) {
			t.Errorf("added: got %+v, want %+v", got, wantPlus)
		}
		if got := one(t, minus); !reflect.DeepEqual(got, wantMinus) {
			t.Errorf("removed: got %+v, want %+v", got, wantMinus)
		}
	})
	t.Run("a file at the bound is compared", func(t *testing.T) {
		t.Parallel()
		got := one(t, added("a.txt", fits))
		if got.TooLarge || len(got.Hunks) != 1 || got.LinesAdded != 20_000 {
			t.Fatalf("got %d hunks, TooLarge=%v, +%d", len(got.Hunks), got.TooLarge, got.LinesAdded)
		}
	})
	t.Run("an added file is not limited by the edit distance", func(t *testing.T) {
		t.Parallel()
		got := one(t, added("a.txt", differ("n", 5000)))
		if got.TooLarge || got.LinesAdded != 5000 || len(got.Hunks) != 1 {
			t.Fatalf("got TooLarge=%v +%d with %d hunks, want an exact 5000", got.TooLarge, got.LinesAdded, len(got.Hunks))
		}
	})
	t.Run("a pair of files too different to compare is too large", func(t *testing.T) {
		t.Parallel()
		// Two files with no line in common take as many edits as both have lines: 500+500 is the bound.
		if got := one(t, mod("a.txt", differ("o", 500), differ("n", 500))); got.TooLarge || got.LinesAdded != 500 || got.LinesRemoved != 500 {
			t.Fatalf("at the bound: got TooLarge=%v +%d -%d", got.TooLarge, got.LinesAdded, got.LinesRemoved)
		}
		c := mod("a.txt", differ("o", 501), differ("n", 501))
		want := report.File{Path: "a.txt", Status: report.Modified, OldSize: len(c.Old), NewSize: len(c.New), TooLarge: true}
		if got := one(t, c); !reflect.DeepEqual(got, want) {
			t.Fatalf("past the bound: got %+v, want %+v", got, want)
		}
	})
}

func TestBuildCutsLongLines(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("€", 200) // 600 bytes; byte 500 falls inside a rune
	got := one(t, added("a.txt", line+"\nshort\n"))
	lines := got.Hunks[0].Lines
	if want := strings.Repeat("€", 166); lines[0].Text != want || !lines[0].Cut {
		t.Fatalf("long line: got %d bytes, Cut=%v; want %d bytes cut on a rune start", len(lines[0].Text), lines[0].Cut, len(want))
	}
	if lines[1].Text != "short" || lines[1].Cut {
		t.Fatalf("short line: got %+v", lines[1])
	}
	exact := one(t, added("a.txt", strings.Repeat("a", 500)+"\n")).Hunks[0].Lines[0]
	if len(exact.Text) != 500 || exact.Cut {
		t.Fatalf("a line of exactly 500 bytes is whole: got %d bytes, Cut=%v", len(exact.Text), exact.Cut)
	}
}

func TestBuildHunkTextBudget(t *testing.T) {
	t.Parallel()
	// Each file is 10,000 lines of 99 bytes: about 0.99 MB of hunk text, so four fit in the 4 MiB budget and a fifth does not.
	big := strings.Repeat(strings.Repeat("a", 99)+"\n", 10_000)
	changes := []report.FileChange{added("f.txt", "small\n")}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		changes = append(changes, added(name+".txt", big))
	}
	r := build(t, changes...)
	if !r.Truncated {
		t.Fatal("the report must say it left hunks out")
	}
	for _, f := range r.Files {
		if f.Path == "e.txt" {
			if !f.Truncated || len(f.Hunks) != 0 || !f.CountsKnown || f.LinesAdded != 10_000 {
				t.Errorf("e.txt: got Truncated=%v, %d hunks, counts known=%v, +%d", f.Truncated, len(f.Hunks), f.CountsKnown, f.LinesAdded)
			}
		} else if f.Truncated || len(f.Hunks) != 1 {
			t.Errorf("%s: got Truncated=%v, %d hunks; want its hunk", f.Path, f.Truncated, len(f.Hunks))
		}
	}
	if r.Totals.LinesAdded != 5*10_000+1 {
		t.Errorf("totals keep the counts of a file whose hunks were left out: got +%d", r.Totals.LinesAdded)
	}
}

func TestBuildMarkerListIsBoundedButCountsAreExact(t *testing.T) {
	t.Parallel()
	r := build(t, removed("a.py", strings.Repeat("assert x\n", 1500)))
	if got := r.Totals.Markers[report.MarkRemovedAssertion]; got != 1500 {
		t.Errorf("marker count = %d, want the exact 1500", got)
	}
	if got := len(r.Files[0].Markers); got != 1000 {
		t.Errorf("listed markers = %d, want 1000", got)
	}
	if !r.Truncated {
		t.Error("the report must say it left markers out")
	}
	if small := build(t, removed("a.py", "assert x\n")); small.Truncated {
		t.Error("a report with room for its markers is not truncated")
	}
}

func TestBuildOrdersAndTotals(t *testing.T) {
	t.Parallel()
	r := build(t,
		mod("b/two.py", "a\nassert 1\n", "a\n"),
		removed("z/gone_test.go", "func TestA(t *testing.T) {\n\tassert.True(t, ok)\n}\n"),
		added("a.txt", "x\ny\nz\n"),
		mod("b/one.js", "a\n", "a\nit.skip('x')\nit.only('y')\n"),
		mod("same.txt", "s\n", "s\n"),
		mod("bin.dat", "\x00", "\x01\x00"),
	)
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	if want := []string{"a.txt", "b/one.js", "b/two.py", "bin.dat", "same.txt", "z/gone_test.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("order = %v, want %v", paths, want)
	}
	wantTotals := report.Totals{
		Files:      map[report.Status]int{report.Added: 1, report.Modified: 4, report.Removed: 1},
		LinesAdded: 3 + 2, LinesRemoved: 1 + 3, // the binary file counts for neither
		Markers: map[report.MarkerKind]int{
			report.MarkRemovedAssertion: 2, report.MarkAddedSkip: 1, report.MarkFocus: 1,
			report.MarkDeletedTest: 1, report.MarkTestFileRemoved: 1,
		},
	}
	if !reflect.DeepEqual(r.Totals, wantTotals) {
		t.Fatalf("totals = %+v, want %+v", r.Totals, wantTotals)
	}
	if r.Truncated {
		t.Fatal("a small report is not truncated")
	}
}

func TestBuildDoesNotChangeItsInput(t *testing.T) {
	t.Parallel()
	changes := []report.FileChange{added("b.txt", "x\n"), added("a.txt", "y\n")}
	build(t, changes...)
	if changes[0].Path != "b.txt" || changes[1].Path != "a.txt" {
		t.Fatalf("Build reordered the caller's slice: %+v", changes)
	}
}

func TestBuildNoChanges(t *testing.T) {
	t.Parallel()
	r := build(t)
	if len(r.Files) != 0 || r.Totals.LinesAdded != 0 || r.Totals.LinesRemoved != 0 || len(r.Totals.Files) != 0 || len(r.Totals.Markers) != 0 {
		t.Fatalf("got %+v, want an empty report", r)
	}
}

func TestBuildRejectsInvalidChanges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		changes []report.FileChange
	}{
		{"zero status", []report.FileChange{{Path: "a", New: []byte("x")}}},
		{"unknown status", []report.FileChange{{Path: "a", Status: "renamed", New: []byte("x")}}},
		{"an added file with a canonical side", []report.FileChange{{Path: "a", Status: report.Added, Old: []byte("x"), New: []byte("y")}}},
		{"a removed file with a proposal side", []report.FileChange{{Path: "a", Status: report.Removed, Old: []byte("x"), New: []byte("y")}}},
		{"a repeated path", []report.FileChange{added("a", "x"), removed("b", "x"), added("a", "y")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := report.Build(tc.changes); !errors.Is(err, report.ErrInvalidChange) {
				t.Fatalf("got %v, want ErrInvalidChange", err)
			}
		})
	}
}
