// Package report computes the change report of the protected files: what differs, line by line, between the canonical
// version and a proposal revision (the "change report" bullet of docs/plan/README.md, owner decision 2026-10-22).
// It is pure: the caller reads the bytes of both sides and this package never touches a file, a database or the
// network. Everything in a report is a deterministic fact about the bytes. A Marker is a line to look at, never a
// verdict, and no agent analysis is involved. File contents are untrusted: they are kept as data, and the caller
// escapes them for the medium it renders to.
package report

import "errors"

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

// Build computes the report of changes, in path order whatever the order of changes.
func Build(changes []FileChange) (Report, error) {
	return Report{}, errors.New("not implemented")
}
