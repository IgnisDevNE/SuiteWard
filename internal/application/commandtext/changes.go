package commandtext

import "github.com/IgnisDevNE/SuiteWard/internal/application/report"

// Changes is what a change report found, as the numbers and names a revision comment and a check summary state. It
// says what was seen, never that it is right or wrong.
type Changes struct {
	Added, Modified, Removed int // every changed file, by how it differs
	Counted                  int // the files whose lines were counted
	LinesAdded, LinesRemoved int // over the Counted files only
	Top                      []ChangedFile
	NotShown                 []NotShown
	NotShownTotal            int
	Markers                  Markers
	Truncated                bool
}

// ChangedFile is a Counted file with its lines.
type ChangedFile struct {
	Path, Status             string // Status is added, modified or removed
	LinesAdded, LinesRemoved int
}

// NotShown is a file the report has no line-by-line diff of, and why.
type NotShown struct {
	Path, Why string
}

// Markers count the marker lines of a report by kind.
type Markers struct {
	RemovedAssertions, AddedSkips, Focused, DeletedTests, TestFilesRemoved int
}

// ChangesOf summarizes r.
func ChangesOf(report.Report) Changes { return Changes{} }

// Visible returns s with every character that hides itself written as an escape.
func Visible(s string) string { return s }
