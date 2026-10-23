package report

import (
	"bytes"
	"slices"
	"strings"
	"unicode/utf8"
)

// entry is one line of the edit script of two versions: kind says which versions have it, oldBefore and newBefore
// count the lines of each version before it, and text is the line with its terminator, if it has one.
type entry struct {
	kind                 LineKind
	oldBefore, newBefore int
	text                 string
}

// splitLines splits s after each "\n", so that a last line without one stays distinct from the same line with one.
func splitLines(s string) []string {
	var lines []string
	for len(s) > 0 {
		end := strings.IndexByte(s, '\n') + 1
		if end == 0 {
			end = len(s)
		}
		lines = append(lines, s[:end])
		s = s[end:]
	}
	return lines
}

// lineCount is len(splitLines(string(b))) without building them.
func lineCount(b []byte) int {
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func tooLarge(b []byte) bool { return len(b) > maxFileBytes || lineCount(b) > maxFileLines }

// cutAt cuts s to at most n bytes, not in the middle of a UTF-8 sequence, and says whether it cut.
func cutAt(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for back := 0; back < utf8.UTFMax-1 && n > 0 && !utf8.RuneStart(s[n]); back++ {
		n--
	}
	return s[:n], true
}

// diff marks the lines of a that are not in b (del) and the lines of b that are not in a (ins): a minimal edit
// script, found with the Myers O(ND) algorithm. ok is false when the edit distance passes maxEditDistance or the
// steps left in work run out; the steps it took are taken from work.
func diff(a, b []int, work *int) (del, ins []bool, ok bool) {
	del, ins = make([]bool, len(a)), make([]bool, len(b))
	lo := 0
	for lo < len(a) && lo < len(b) && a[lo] == b[lo] {
		lo++
	}
	hiA, hiB := len(a), len(b)
	for hiA > lo && hiB > lo && a[hiA-1] == b[hiB-1] {
		hiA--
		hiB--
	}
	switch lo {
	case hiA: // nothing of a is left: all that is left of b is inserted, however long
		setAll(ins[lo:hiB])
		return del, ins, true
	case hiB:
		setAll(del[lo:hiA])
		return del, ins, true
	}
	return del, ins, myers(a[lo:hiA], b[lo:hiB], del[lo:hiA], ins[lo:hiB], work)
}

func setAll(marks []bool) {
	for i := range marks {
		marks[i] = true
	}
}

// myers finds a shortest edit script of a into b and marks it in del and ins. v[off+k] is the furthest x reached on
// diagonal k = x-y, and trace[d-1] keeps the diagonals that round d-1 reached, to walk the script back.
func myers(a, b []int, del, ins []bool, work *int) bool {
	n, m := len(a), len(b)
	limit := min(n+m, maxEditDistance)
	off := limit + 1
	v := make([]int, 2*limit+3)
	trace := make([][]int, 0, limit)
	for d := 0; d <= limit; d++ {
		if d > 0 {
			trace = append(trace, slices.Clone(v[off-d+1:off+d]))
		}
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // down: insert b[y]
			} else {
				x = v[off+k-1] + 1 // right: delete a[x]
			}
			start := x
			for y := x - k; x < n && y < m && a[x] == b[y]; y++ {
				x++
			}
			v[off+k] = x
			if *work -= 1 + x - start; *work < 0 {
				return false
			}
			if x >= n && x-k >= m {
				walkBack(trace, x, x-k, del, ins)
				return true
			}
		}
	}
	return false
}

// walkBack follows the diagonals of trace back from (x, y) to the start and marks each move: right deletes a[x] and
// down inserts b[y].
func walkBack(trace [][]int, x, y int, del, ins []bool) {
	for d := len(trace); d > 0; d-- {
		prev := trace[d-1] // diagonal k of round d-1 is at prev[k+d-1]
		k := x - y
		pk := k - 1
		if k == -d || (k != d && prev[k-1+d-1] < prev[k+1+d-1]) {
			pk = k + 1
		}
		px := prev[pk+d-1]
		py := px - pk
		for x > px && y > py { // the run of equal lines after the move
			x--
			y--
		}
		if x == px {
			ins[py] = true
		} else {
			del[px] = true
		}
		x, y = px, py
	}
}

// script walks a and b along the marks of diff: the lines of a that were deleted come out first, then the lines of
// b that were inserted, at each change.
func script(a, b []string, del, ins []bool) []entry {
	out := make([]entry, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && del[i]:
			out = append(out, entry{LineRemoved, i, j, a[i]})
			i++
		case j < len(b) && ins[j]:
			out = append(out, entry{LineAdded, i, j, b[j]})
			j++
		default:
			out = append(out, entry{LineContext, i, j, a[i]})
			i++
			j++
		}
	}
	return out
}

// span is the range of entries of one hunk, both ends included.
type span struct{ from, to int }

// spansOf groups the changes of script with three lines of context on each side; changes with at most six lines
// between them share a span.
func spansOf(script []entry) []span {
	var spans []span
	for i, e := range script {
		if e.kind == LineContext {
			continue
		}
		from, to := max(i-contextLines, 0), min(i+contextLines, len(script)-1)
		if n := len(spans); n > 0 && from <= spans[n-1].to+1 {
			spans[n-1].to = to
		} else {
			spans = append(spans, span{from, to})
		}
	}
	return spans
}

// stored is a line as a hunk keeps it: without its terminator and cut to the stored line limit.
func stored(e entry) (text string, terminated, cut bool) {
	text, terminated = strings.CutSuffix(e.text, "\n")
	text, cut = cutAt(text, maxLineBytes)
	return text, terminated, cut
}

// hunkCost is what the hunks of spans cost against the size budget of a report, known before any line is built.
func hunkCost(script []entry, spans []span) int {
	cost := 0
	for _, s := range spans {
		for _, e := range script[s.from : s.to+1] {
			text, _, _ := stored(e)
			cost += len(text) + lineOverhead
		}
	}
	return cost
}

func hunksOf(script []entry, spans []span) []Hunk {
	var hunks []Hunk
	for _, s := range spans {
		hunks = append(hunks, hunkOf(script[s.from:s.to+1]))
	}
	return hunks
}

func hunkOf(es []entry) Hunk {
	h := Hunk{OldStart: es[0].oldBefore, NewStart: es[0].newBefore, Lines: make([]Line, len(es))}
	for i, e := range es {
		text, terminated, cut := stored(e)
		// A clone, so that a kept line does not hold the whole file it was cut from.
		h.Lines[i] = Line{Kind: e.kind, Text: strings.Clone(text), NoNewline: !terminated, Cut: cut}
		if e.kind != LineAdded {
			h.OldLines++
		}
		if e.kind != LineRemoved {
			h.NewLines++
		}
	}
	// A side with no lines starts at the line before the hunk, as in a unified diff.
	if h.OldLines > 0 {
		h.OldStart++
	}
	if h.NewLines > 0 {
		h.NewStart++
	}
	return h
}
