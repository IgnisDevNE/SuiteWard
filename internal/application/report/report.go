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
	maxDiffWork     = 300_000_000 // steps of every comparison of one report together, about a second
	contextLines    = 3
	maxLineBytes    = 500
	maxMarkerBytes  = 200
	maxHunkBytes    = 4 << 20 // text of the hunks of one report
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
// empty or absent: the two are the same here.
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
// it is 0, with no Text, for MarkTestFileRemoved. Text is the trimmed line, cut to 200 bytes.
type Marker struct {
	Kind MarkerKind
	Path string
	Line int
	Text string
}

// File is the report of one FileChange. OldSize and NewSize are byte lengths. LinesAdded and LinesRemoved are exact
// only when CountsKnown: a binary file, and a modified file too large or too different to compare within the
// bounds (TooLarge), have unknown counts, never zero. An added or removed file over the size bound has Hunks
// omitted but its counts are exact, since counting lines is cheap. Truncated says the Hunks were left out to keep the
// report within its text budget; the counts stay exact.
type File struct {
	Path                        string
	Status                      Status
	OldSize, NewSize            int
	LinesAdded, LinesRemoved    int
	CountsKnown                 bool
	Binary, TooLarge, Truncated bool
	Hunks                       []Hunk
	Markers                     []Marker
}

// Totals sum a report. LinesAdded and LinesRemoved cover the files with CountsKnown only. Markers counts every
// marker found, including the ones the Report left out of the lists of its files.
type Totals struct {
	Files                    map[Status]int
	LinesAdded, LinesRemoved int
	Markers                  map[MarkerKind]int
}

// Report is the change report: Files in path order. Truncated says a file's hunks or some markers were left out to
// keep the report bounded; the totals stay exact.
type Report struct {
	Files     []File
	Totals    Totals
	Truncated bool
}

// ErrInvalidChange reports a FileChange the caller built wrongly.
var ErrInvalidChange = errors.New("invalid file change")

func (c FileChange) validate() error {
	switch {
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
func Build(changes []FileChange) (Report, error) {
	sorted := slices.Clone(changes)
	slices.SortStableFunc(sorted, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	b := &builder{
		files:      make([]File, 0, len(sorted)),
		totals:     Totals{Files: map[Status]int{}, Markers: map[MarkerKind]int{}},
		hunkRoom:   maxHunkBytes,
		workRoom:   maxDiffWork,
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
	f := File{Path: c.Path, Status: c.Status, OldSize: len(c.Old), NewSize: len(c.New)}
	b.totals.Files[c.Status]++
	if c.Status == Removed && isTestPath(c.Path) {
		b.mark(&f, Marker{Kind: MarkTestFileRemoved, Path: c.Path})
	}
	if script, ok := b.compare(&f, c); ok {
		f.CountsKnown = true
		for _, e := range script {
			switch e.kind {
			case LineAdded:
				f.LinesAdded++
			case LineRemoved:
				f.LinesRemoved++
			}
		}
		b.markLines(&f, script)
		b.keepHunks(&f, script)
	}
	if f.CountsKnown {
		b.totals.LinesAdded += f.LinesAdded
		b.totals.LinesRemoved += f.LinesRemoved
	}
	b.files = append(b.files, f)
}

// compare aligns the two sides of c, or says why it cannot on f: a binary file has no lines, and a file too large or
// too different has too many to compare. An added or removed file that is too large still has an exact count.
func (b *builder) compare(f *File, c FileChange) ([]entry, bool) {
	switch {
	case bytes.IndexByte(c.Old, 0) >= 0 || bytes.IndexByte(c.New, 0) >= 0:
		f.Binary = true
		return nil, false
	case tooLarge(c.Old) || tooLarge(c.New):
		f.TooLarge = true
		if c.Status != Modified {
			f.LinesAdded, f.LinesRemoved, f.CountsKnown = lineCount(c.New), lineCount(c.Old), true
		}
		return nil, false
	}
	before, after := splitLines(string(c.Old)), splitLines(string(c.New))
	ids := map[string]int{}
	del, ins, ok := diff(intern(ids, before), intern(ids, after), &b.workRoom)
	if !ok {
		f.TooLarge = true
		return nil, false
	}
	return script(before, after, del, ins), true
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

// keepHunks gives f the hunks of script unless their text would pass what the report has room for.
func (b *builder) keepHunks(f *File, script []entry) {
	hunks := hunksOf(script)
	size := 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			size += len(l.Text)
		}
	}
	if size > b.hunkRoom {
		f.Truncated, b.truncated = true, true
		return
	}
	b.hunkRoom -= size
	f.Hunks = hunks
}

// markLines marks the changed lines of script.
func (b *builder) markLines(f *File, script []entry) {
	rules := rulesFor(f.Path)
	for _, e := range script {
		line := e.oldBefore + 1
		if e.kind == LineAdded {
			line = e.newBefore + 1
		}
		for _, kind := range rules.kinds(e.kind, e.text) {
			text, _ := cutAt(strings.TrimSpace(e.text), maxMarkerBytes)
			b.mark(f, Marker{Kind: kind, Path: f.Path, Line: line, Text: text})
		}
	}
}

// mark counts m and lists it on f while the report has room for markers.
func (b *builder) mark(f *File, m Marker) {
	b.totals.Markers[m.Kind]++
	if b.markerRoom == 0 {
		b.truncated = true
		return
	}
	b.markerRoom--
	f.Markers = append(f.Markers, m)
}
