package report_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

// FuzzDiff checks the contract of a report on any pair of files: it does not panic, its hunks rebuild the proposal
// from the canonical version, its counts are the hunk lines, and its diff has the fewest edits there are.
func FuzzDiff(f *testing.F) {
	for _, seed := range []struct {
		status    byte
		old, next string
	}{
		{1, "", ""}, {1, "a\n", "a\n"}, {0, "", "a\nb\n"}, {2, "a\nb\n", ""}, {1, "a\nb", "a\nb\n"}, {1, "a\r\nb\r\n", "a\nb\n"},
		{1, "a\nb\nc\nd\n", "a\nx\nc\ny\n"}, {1, "a\n\n\nb\n", "b\n\n\na\n"}, {1, "x\nx\nx\n", "x\nx\n"}, {1, "a\x00b", "a\nb"},
		{1, strings.Repeat("l\n", 30) + "m", "n\n" + strings.Repeat("l\n", 30)},
		{1, "assert x\nfunc TestA() {\nb\n", "b\n  assert x\nt.Skip()\n"}, {2, "func TestA() {\n\tt.Fatal()\n}\n", ""}, {0, "", "t.Skip()\nt.Skip()\n"},
	} {
		f.Add(seed.status, seed.old, seed.next)
	}
	f.Fuzz(func(t *testing.T, status byte, old, next string) {
		c := report.FileChange{Path: "f.go", Status: [...]report.Status{report.Added, report.Modified, report.Removed}[status%3]}
		if c.Status != report.Added {
			c.Old = []byte(old)
		}
		if c.Status != report.Removed {
			c.New = []byte(next)
		}
		r, err := report.Build([]report.FileChange{c})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		got := r.Files[0]
		if (got.Limit == report.LimitBinary) != (bytes.IndexByte(c.Old, 0) >= 0 || bytes.IndexByte(c.New, 0) >= 0) {
			t.Fatalf("Limit = %q for %q and %q", got.Limit, c.Old, c.New)
		}
		if got.Limit == report.LimitBinary {
			return
		}
		if got.Limit != "" && len(c.Old)+len(c.New) < 1000 {
			t.Fatalf("%d bytes are never too large", len(c.Old)+len(c.New))
		}
		checkMarkers(t, c, r)
		if got.Limit != "" || got.Truncated {
			return
		}
		checkHunks(t, c, got)
		if oldLines, newLines := splitAfter(string(c.Old)), splitAfter(string(c.New)); len(oldLines) <= 300 && len(newLines) <= 300 {
			if want := len(oldLines) + len(newLines) - 2*lcs(oldLines, newLines); got.LinesAdded+got.LinesRemoved != want {
				t.Fatalf("%d edits for %q to %q, the fewest are %d", got.LinesAdded+got.LinesRemoved, c.Old, c.New, want)
			}
		}
	})
}

// checkHunks applies the hunks of got to c.Old and requires c.New, and requires the numbering and the counts to agree
// with the lines of the hunks.
func checkHunks(t *testing.T, c report.FileChange, got report.File) {
	t.Helper()
	for _, h := range got.Hunks {
		for _, l := range h.Lines {
			if l.Cut {
				return // a cut line cannot rebuild its file
			}
		}
	}
	rebuilt, plus, minus, err := apply(string(c.Old), got.Hunks)
	if err != nil {
		t.Fatalf("hunks of %q to %q: %v", c.Old, c.New, err)
	}
	if rebuilt != string(c.New) {
		t.Fatalf("hunks of %q to %q rebuild %q", c.Old, c.New, rebuilt)
	}
	if plus != got.LinesAdded || minus != got.LinesRemoved || !got.CountsKnown {
		t.Fatalf("counts +%d -%d (known %v), hunk lines +%d -%d", got.LinesAdded, got.LinesRemoved, got.CountsKnown, plus, minus)
	}
}

// checkMarkers requires that every marker of the one file of r names a line of the side its kind belongs to, with that
// line's trimmed text, and that the totals count the markers listed.
func checkMarkers(t *testing.T, c report.FileChange, r report.Report) {
	t.Helper()
	oldLines, newLines := splitAfter(string(c.Old)), splitAfter(string(c.New))
	for _, m := range r.Files[0].Markers {
		lines := oldLines
		if m.Kind == report.MarkAddedSkip || m.Kind == report.MarkFocus {
			lines = newLines
		}
		if m.Path != c.Path || m.Line < 1 || m.Line > len(lines) {
			t.Fatalf("marker %+v does not name a line of %q or %q", m, c.Old, c.New)
		}
		trimmed := strings.TrimSpace(lines[m.Line-1])
		if !strings.HasPrefix(trimmed, m.Text) || len(m.Text) > 200 || (len(trimmed) <= 200 && trimmed != m.Text) {
			t.Fatalf("marker %+v is not the trimmed line %q", m, trimmed)
		}
	}
	count := 0
	for _, n := range r.Totals.Markers {
		count += n
	}
	if !r.Truncated && count != len(r.Files[0].Markers) {
		t.Fatalf("totals count %d markers, the file lists %d", count, len(r.Files[0].Markers))
	}
}

// splitAfter splits s into its lines, each with its terminator; the last has none when s does not end with one.
func splitAfter(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// rebuild is the proposal in the making while hunks are applied to the lines of the canonical version.
type rebuild struct {
	old                       []string
	out                       strings.Builder
	pos, written, plus, minus int
}

// apply rebuilds the proposal from old and hunks, checking that every context and removed line is the old line it
// claims and that every hunk header numbers its lines as a unified diff does.
func apply(old string, hunks []report.Hunk) (rebuilt string, plus, minus int, err error) {
	r := &rebuild{old: splitAfter(old)}
	for i, h := range hunks {
		if err := r.hunk(i, h); err != nil {
			return "", 0, 0, err
		}
	}
	r.copyTo(len(r.old))
	return r.out.String(), r.plus, r.minus, nil
}

// copyTo copies the unchanged old lines up to old line index end.
func (r *rebuild) copyTo(end int) {
	for ; r.pos < end; r.pos++ {
		r.out.WriteString(r.old[r.pos])
		r.written++
	}
}

func (r *rebuild) hunk(i int, h report.Hunk) error {
	start := h.OldStart - 1
	if h.OldLines == 0 {
		start = h.OldStart
	}
	if start < r.pos || start > len(r.old) {
		return fmt.Errorf("hunk %d starts at old line %d with %d lines done", i, h.OldStart, r.pos)
	}
	r.copyTo(start)
	wantNew := r.written + 1
	if h.NewLines == 0 {
		wantNew = r.written
	}
	if h.NewStart != wantNew {
		return fmt.Errorf("hunk %d has NewStart %d, want %d", i, h.NewStart, wantNew)
	}
	oldCount, newCount := 0, 0
	for _, l := range h.Lines {
		text := l.Text
		if !l.NoNewline {
			text += "\n"
		}
		if l.Kind != report.LineAdded {
			if r.pos >= len(r.old) || r.old[r.pos] != text {
				return fmt.Errorf("hunk %d line %q is not old line %d", i, text, r.pos+1)
			}
			r.pos++
			oldCount++
		}
		if l.Kind != report.LineRemoved {
			r.out.WriteString(text)
			r.written++
			newCount++
		}
		switch l.Kind {
		case report.LineAdded:
			r.plus++
		case report.LineRemoved:
			r.minus++
		}
	}
	if oldCount != h.OldLines || newCount != h.NewLines {
		return fmt.Errorf("hunk %d counts %d/%d old/new lines, its header says %d/%d", i, oldCount, newCount, h.OldLines, h.NewLines)
	}
	return nil
}

// lcs is the length of the longest common subsequence of a and b.
func lcs(a, b []string) int {
	row := make([]int, len(b)+1)
	for i := range a {
		diagonal := 0
		for j := range b {
			above := row[j+1]
			if a[i] == b[j] {
				row[j+1] = diagonal + 1
			} else {
				row[j+1] = max(row[j+1], row[j])
			}
			diagonal = above
		}
	}
	return row[len(b)]
}
