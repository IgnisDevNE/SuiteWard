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
		if got.Binary != (bytes.IndexByte(c.Old, 0) >= 0 || bytes.IndexByte(c.New, 0) >= 0) {
			t.Fatalf("Binary = %v for %q and %q", got.Binary, c.Old, c.New)
		}
		if got.Binary {
			return
		}
		if got.TooLarge && len(c.Old)+len(c.New) < 1000 {
			t.Fatalf("%d bytes are never too large", len(c.Old)+len(c.New))
		}
		if got.TooLarge || got.Truncated {
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

// splitAfter splits s into its lines, each with its terminator; the last has none when s does not end with one.
func splitAfter(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// apply rebuilds the proposal from old and hunks, checking that every context and removed line is the old line it
// claims and that every hunk header numbers its lines as a unified diff does.
func apply(old string, hunks []report.Hunk) (rebuilt string, plus, minus int, err error) {
	oldLines := splitAfter(old)
	var out strings.Builder
	pos, written := 0, 0
	for i, h := range hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			start = h.OldStart
		}
		if start < pos || start > len(oldLines) {
			return "", 0, 0, fmt.Errorf("hunk %d starts at old line %d with %d lines done", i, h.OldStart, pos)
		}
		for ; pos < start; pos++ {
			out.WriteString(oldLines[pos])
			written++
		}
		wantNew := written + 1
		if h.NewLines == 0 {
			wantNew = written
		}
		if h.NewStart != wantNew {
			return "", 0, 0, fmt.Errorf("hunk %d has NewStart %d, want %d", i, h.NewStart, wantNew)
		}
		oldCount, newCount := 0, 0
		for _, l := range h.Lines {
			text := l.Text
			if !l.NoNewline {
				text += "\n"
			}
			if l.Kind != report.LineAdded {
				if pos >= len(oldLines) || oldLines[pos] != text {
					return "", 0, 0, fmt.Errorf("hunk %d line %q is not old line %d", i, text, pos+1)
				}
				pos++
				oldCount++
			}
			if l.Kind != report.LineRemoved {
				out.WriteString(text)
				written++
				newCount++
			}
			switch l.Kind {
			case report.LineAdded:
				plus++
			case report.LineRemoved:
				minus++
			}
		}
		if oldCount != h.OldLines || newCount != h.NewLines {
			return "", 0, 0, fmt.Errorf("hunk %d counts %d/%d old/new lines, its header says %d/%d", i, oldCount, newCount, h.OldLines, h.NewLines)
		}
	}
	for ; pos < len(oldLines); pos++ {
		out.WriteString(oldLines[pos])
	}
	return out.String(), plus, minus, nil
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
