// Package report computes the change report of the protected files: what differs, line by line, between the canonical
// version and a proposal revision (the "change report" bullet of docs/plan/README.md, owner decision 2026-10-22).
// It is pure: the caller reads the bytes of both sides and this package never touches a file, a database or the
// network. Everything in a report is a deterministic fact about the bytes. A Marker is a line to look at, never a
// verdict, and no agent analysis is involved. File contents are untrusted: they are kept as data, and the caller
// escapes them for the medium it renders to.
package report

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The bounds that keep a report, and the work to build it, small whatever the files are. The files are untrusted: a
// proposal can hold hundreds of protected files built to be slow to compare.
const (
	maxFileBytes    = 1 << 20     // a side over it is too large to compare
	maxFileLines    = 20_000      // likewise
	maxEditDistance = 1000        // lines to delete plus lines to insert; past it a pair of files is too different
	maxDiffWork     = 300_000_000 // steps (a diagonal tried, a line compared) of all the comparisons of one report: about 580 pairs at the edit distance bound, which took 1.6 s to build
	contextLines    = 3
	maxLineBytes    = 500
	maxMarkerBytes  = 200
	maxHunkBytes    = 4 << 20 // what the hunks of one report cost: every line costs its text and lineOverhead
	lineOverhead    = 32      // a stored Line without its text; it makes a report of empty lines cost what it holds
	maxMarkers      = 1000    // markers listed in one report; the counts of Totals stay exact
)

// Status is how a file differs between the two versions; its zero value is invalid.
type Status string

const (
	Added    Status = "added"
	Modified Status = "modified"
	Removed  Status = "removed"
)

// FileChange is one protected file that differs between the canonical version and the proposal. Old is the canonical
// content and New the proposal's. The side a status does not have (Old of an added file, New of a removed one) is
// empty or absent: the two are the same here. Path is slash-separated, as a git path is; an empty path or one with a
// backslash is rejected.
type FileChange struct {
	Path     string
	Status   Status
	Old, New []byte
}

// LineKind is how a line of a hunk relates to the two versions: its byte is the unified diff prefix.
type LineKind byte

const (
	LineContext LineKind = ' '
	LineAdded   LineKind = '+'
	LineRemoved LineKind = '-'
)

// Line is one line of a hunk. Text has no trailing "\n" but keeps a "\r". NoNewline marks the last line of a file
// that has no line terminator; Cut marks a Text that was cut to the stored line limit.
type Line struct {
	Kind      LineKind
	Text      string
	NoNewline bool
	Cut       bool
}

// Hunk is a run of changed lines with up to three lines of context on each side, numbered as in a unified diff: a
// side with no lines starts at the line before the hunk (an added file has OldStart 0).
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line
}

// MarkerKind names what a Marker saw.
type MarkerKind string

const (
	MarkRemovedAssertion MarkerKind = "removed-assertion"
	MarkAddedSkip        MarkerKind = "added-skip"
	MarkFocus            MarkerKind = "focus"
	MarkDeletedTest      MarkerKind = "deleted-test"
	MarkTestFileRemoved  MarkerKind = "test-file-removed"
)

// Marker is a deterministic observation about one changed line, keyed by the file's extension: it says what was
// seen, never that it is wrong. Line is the old side's line number for a removal and the new side's for an addition;
// it is 0, with no Text, for MarkTestFileRemoved. Text is the trimmed line, cut to 200 bytes. A line that the other
// side has too, as many times and compared without the space around it, only moved or was reindented and gets no
// marker. A file's markers come in this order: MarkTestFileRemoved, the removals by old line, the additions by new
// line.
type Marker struct {
	Kind MarkerKind
	Path string
	Line int
	Text string
}

// Limit says why a file was not compared line by line; its zero value means it was.
type Limit string

const (
	// LimitBinary: a NUL byte in a side. There are no lines, hunks or markers (but MarkTestFileRemoved).
	LimitBinary Limit = "binary"
	// LimitSize: a side over 1 MiB or 20,000 lines. There are no hunks or line markers; an added or removed file still
	// has exact counts.
	LimitSize Limit = "too-large"
	// LimitEditDistance: the two sides differ by more than 1000 lines. The file has no hunks and its counts are unknown.
	LimitEditDistance Limit = "edit-distance"
	// LimitBudget: the work shared by the files of the report ran out before this one. Same as LimitEditDistance, and
	// Report.Truncated is set.
	LimitBudget Limit = "budget"
)

// File is the report of one FileChange. OldSize and NewSize are byte lengths. LinesAdded and LinesRemoved are exact
// only when CountsKnown: a file with a Limit has unknown counts, never zero, except an added or removed file over
// the size bound, which has exact counts since counting lines is cheap. A modified file stopped by LimitEditDistance
// or LimitBudget still has its markers, taken from the lines that one side has and the other lacks; the size bound and
// a binary file are the only limits that leave a file without line markers. Truncated says the Hunks were left out to
// keep the report within its size budget, in which a line costs its text and 32 bytes; the counts stay exact.
// MarkersTruncated says the report's limit on listed markers dropped some of this file's.
type File struct {
	Path                     string
	Status                   Status
	OldSize, NewSize         int
	LinesAdded, LinesRemoved int
	CountsKnown              bool
	Limit                    Limit
	Truncated                bool
	MarkersTruncated         bool
	Hunks                    []Hunk
	Markers                  []Marker
}

// Totals sum a report. LinesAdded and LinesRemoved cover the files with CountsKnown only, so a caller that prints
// them must also say how many files have a Limit. Markers counts every marker found, including the ones the Report left
// out of the lists of its files.
type Totals struct {
	Files                    map[Status]int
	LinesAdded, LinesRemoved int
	Markers                  map[MarkerKind]int
}

// Report is the change report: Files in path order. Truncated says that a budget shared by the files left something
// out: some hunks (File.Truncated), some markers (File.MarkersTruncated), or the comparison of some files
// (LimitBudget). The totals of markers stay exact, the counts of lines only for the files that have them.
type Report struct {
	Files     []File
	Totals    Totals
	Truncated bool
}

// ErrInvalidChange reports a FileChange the caller built wrongly.
var ErrInvalidChange = errors.New("invalid file change")

func (c FileChange) validate() error {
	switch {
	case c.Path == "" || strings.Contains(c.Path, `\`):
		return fmt.Errorf("%w: %q is not a slash-separated path", ErrInvalidChange, c.Path)
	case c.Status != Added && c.Status != Modified && c.Status != Removed:
		return fmt.Errorf("%w: %q has the status %q", ErrInvalidChange, c.Path, c.Status)
	case c.Status == Added && len(c.Old) > 0:
		return fmt.Errorf("%w: %q is added but has a canonical side", ErrInvalidChange, c.Path)
	case c.Status == Removed && len(c.New) > 0:
		return fmt.Errorf("%w: %q is removed but has a proposal side", ErrInvalidChange, c.Path)
	}
	return nil
}

// Build computes the report of changes, in path order whatever the order of changes. Files are compared one after
// the other in that order and share the budgets, so the same changes always give the same report.
func Build(changes []FileChange) (Report, error) { return build(changes, maxDiffWork) }

// build is Build with the work budget a test can shrink.
func build(changes []FileChange, work int) (Report, error) {
	sorted := slices.Clone(changes)
	slices.SortStableFunc(sorted, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	b := &builder{
		files:      make([]File, 0, len(sorted)),
		totals:     Totals{Files: map[Status]int{}, Markers: map[MarkerKind]int{}},
		hunkRoom:   maxHunkBytes,
		workRoom:   work,
		markerRoom: maxMarkers,
	}
	for i, c := range sorted {
		if err := c.validate(); err != nil {
			return Report{}, err
		}
		if i > 0 && sorted[i-1].Path == c.Path {
			return Report{}, fmt.Errorf("%w: %q is listed twice", ErrInvalidChange, c.Path)
		}
		b.add(c)
	}
	return Report{Files: b.files, Totals: b.totals, Truncated: b.truncated}, nil
}

// builder is a report in the making, with what is left of each budget.
type builder struct {
	files                          []File
	totals                         Totals
	truncated                      bool
	hunkRoom, workRoom, markerRoom int
}

func (b *builder) add(c FileChange) {
	f := File{Path: c.Path, Status: c.Status, OldSize: len(c.Old), NewSize: len(c.New), Limit: sideLimit(c)}
	b.totals.Files[c.Status]++
	if c.Status == Removed && isTestPath(c.Path) {
		b.mark(&f, Marker{Kind: MarkTestFileRemoved, Path: c.Path})
	}
	switch {
	case f.Limit == "":
		b.compare(&f, c)
	case f.Limit == LimitSize && c.Status != Modified:
		// Counting lines is cheap, so an added or removed file over the size bound still has exact counts.
		f.LinesAdded, f.LinesRemoved, f.CountsKnown = lineCount(c.New), lineCount(c.Old), true
	}
	if f.CountsKnown {
		b.totals.LinesAdded += f.LinesAdded
		b.totals.LinesRemoved += f.LinesRemoved
	}
	b.files = append(b.files, f)
}

// sideLimit says why c cannot be read line by line at all: a binary file has no lines, and a side over the size bound
// has too many.
func sideLimit(c FileChange) Limit {
	switch {
	case bytes.IndexByte(c.Old, 0) >= 0 || bytes.IndexByte(c.New, 0) >= 0:
		return LimitBinary
	case tooLarge(c.Old) || tooLarge(c.New):
		return LimitSize
	}
	return ""
}

// compare reads the two sides of c line by line. Its diff gives the counts, the hunks and the markers of the changed
// lines. When the diff passes a bound, f says which and the markers come from the lines that one side has and the
// other lacks, so that they survive every bound but the size of a side.
func (b *builder) compare(f *File, c FileChange) {
	before, after := splitLines(string(c.Old)), splitLines(string(c.New))
	ids := map[string]int{}
	del, ins, ok := diff(intern(ids, before), intern(ids, after), &b.workRoom)
	if !ok {
		f.Limit = LimitEditDistance
		if b.workRoom < 0 {
			f.Limit, b.truncated = LimitBudget, true
		}
		b.markLines(f, numberAll(before), numberAll(after))
		return
	}
	edits := script(before, after, del, ins)
	var removed, added []numbered
	for _, e := range edits {
		switch e.kind {
		case LineRemoved:
			removed = append(removed, numbered{e.oldBefore + 1, e.text})
		case LineAdded:
			added = append(added, numbered{e.newBefore + 1, e.text})
		}
	}
	f.LinesRemoved, f.LinesAdded, f.CountsKnown = len(removed), len(added), true
	b.markLines(f, removed, added)
	b.keepHunks(f, edits)
}

// intern numbers the lines of one side by their content, terminator included, so that comparing two lines is
// comparing two numbers and "b" is not "b" with a newline.
func intern(ids map[string]int, lines []string) []int {
	out := make([]int, len(lines))
	for i, l := range lines {
		id, ok := ids[l]
		if !ok {
			id = len(ids)
			ids[l] = id
		}
		out[i] = id
	}
	return out
}

// keepHunks gives f the hunks of edits unless they would cost more than the report has room for.
func (b *builder) keepHunks(f *File, edits []entry) {
	spans := spansOf(edits)
	cost := hunkCost(edits, spans)
	if cost > b.hunkRoom {
		f.Truncated, b.truncated = true, true
		return
	}
	b.hunkRoom -= cost
	f.Hunks = hunksOf(edits, spans)
}

// numbered is a line of one side with its number there, from 1.
type numbered struct {
	no   int
	text string
}

func numberAll(lines []string) []numbered {
	out := make([]numbered, len(lines))
	for i, l := range lines {
		out[i] = numbered{i + 1, l}
	}
	return out
}

// markLines marks the lines that the changes removed and added: first the removals by their old line, then the
// additions by their new line. A line that the other side has too, as many times and compared without the space
// around it, only moved, was reindented or changed its line ending: it is no marker.
func (b *builder) markLines(f *File, removed, added []numbered) {
	rules := rulesFor(f.Path)
	b.markSide(f, rules, LineRemoved, removed, added)
	b.markSide(f, rules, LineAdded, added, removed)
}

func (b *builder) markSide(f *File, rules rules, k LineKind, side, other []numbered) {
	type candidate struct {
		numbered
		kinds []MarkerKind
	}
	var candidates []candidate
	for _, l := range side {
		if kinds := rules.kinds(k, l.text); len(kinds) > 0 {
			candidates = append(candidates, candidate{l, kinds})
		}
	}
	if len(candidates) == 0 {
		return
	}
	counts := map[string]int{}
	for _, l := range other {
		counts[strings.TrimSpace(l.text)]++
	}
	for _, cd := range candidates {
		trimmed := strings.TrimSpace(cd.text)
		if counts[trimmed] > 0 {
			counts[trimmed]--
			continue
		}
		text, _ := cutAt(trimmed, maxMarkerBytes)
		for _, kind := range cd.kinds {
			b.mark(f, Marker{Kind: kind, Path: f.Path, Line: cd.no, Text: strings.Clone(text)})
		}
	}
}

// mark counts m and lists it on f while the report has room for markers.
func (b *builder) mark(f *File, m Marker) {
	b.totals.Markers[m.Kind]++
	if b.markerRoom == 0 {
		f.MarkersTruncated, b.truncated = true, true
		return
	}
	b.markerRoom--
	f.Markers = append(f.Markers, m)
}
