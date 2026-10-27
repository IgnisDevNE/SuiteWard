package run

import (
	"unicode"
	"unicode/utf8"
)

// The bounds of a result (runtime contract, "POST /jobs/{id}/result").
const (
	maxTests      = 10000
	maxMarkers    = 200
	maxSubmodules = 100
	maxUnparsed   = 100
	maxBodyBytes  = 4 << 20

	// The bounds of the strings and the numbers that it carries. A test file and an unparsed file are at most 1024 bytes like the file of a
	// test; the file of a marker and a submodule are paths of the tree, which are at most 4096.
	maxPackageBytes  = 512
	maxNameBytes     = 200
	maxTestFileBytes = 1024
	maxTreePathBytes = 4096
	maxMarkerLine    = 10_000_000
)

// testRow is one test of a complete result.
type testRow struct {
	Package  string   `json:"package"`
	Name     string   `json:"name"`
	File     string   `json:"file"`
	Kind     string   `json:"kind"`
	Head     []string `json:"head,omitempty"`
	Base     []string `json:"base,omitempty"`
	Excluded string   `json:"excluded,omitempty"`
}

// markerRow is a line that the receipt flagged.
type markerRow struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// completion is what a run that completed found.
type completion struct {
	tests      []testRow
	markers    []markerRow
	submodules []string
	unparsed   []string
}

// check refuses a result that POST result would refuse, so that the executor says why and never posts what comes back as a 400. It reads the
// counts and then the fields: a count past its bound, or a string or a number past its bound, is a result that passed its limit
// (too-large); a path that holds a character that the service refuses is a source that the result cannot carry (source-mismatch). The
// markers were cut to their bound before, as lines to read.
func (c completion) check() error {
	switch {
	case len(c.tests) > maxTests:
		return refuse(reasonTooLarge, "the run has %d tests; a result holds at most %d", len(c.tests), maxTests)
	case len(c.submodules) > maxSubmodules:
		return refuse(reasonTooLarge, "the tree has %d submodules; a result lists at most %d", len(c.submodules), maxSubmodules)
	case len(c.unparsed) > maxUnparsed:
		return refuse(reasonTooLarge, "%d changed test files do not parse; a result lists at most %d", len(c.unparsed), maxUnparsed)
	}
	for _, t := range c.tests {
		for _, err := range []error{
			carried("a package", t.Package, maxPackageBytes), carried("a test name", t.Name, maxNameBytes), carried("a test file", t.File, maxTestFileBytes),
		} {
			if err != nil {
				return err
			}
		}
	}
	for _, m := range c.markers {
		if m.Line < 1 || m.Line > maxMarkerLine {
			return refuse(reasonTooLarge, "a marker is at the line %d; a result holds the lines 1 to %d", m.Line, maxMarkerLine)
		}
		if err := carried("the file of a marker", m.Path, maxTreePathBytes); err != nil {
			return err
		}
	}
	for _, p := range c.submodules {
		if err := carried("a submodule", p, maxTreePathBytes); err != nil {
			return err
		}
	}
	for _, p := range c.unparsed {
		if err := carried("an unparsed file", p, maxTestFileBytes); err != nil {
			return err
		}
	}
	return nil
}

// carried checks a path or a name that the result carries: within its bound, and holding no control, format, line separator or paragraph
// separator character, and nothing that is not UTF-8, which the service refuses. It names the field and never the text, which is the
// candidate's.
func carried(what, s string, bound int) error {
	if len(s) > bound {
		return refuse(reasonTooLarge, "%s is %d bytes; a result holds at most %d", what, len(s), bound)
	}
	if !utf8.ValidString(s) {
		return refuse(reasonSourceMismatch, "%s is not UTF-8", what)
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return refuse(reasonSourceMismatch, "%s holds a control, format or separator character, which a result cannot carry", what)
		}
	}
	return nil
}

// capMarkers keeps the first maxMarkers lines: they are lines to read, never a verdict.
func capMarkers(rows []markerRow) []markerRow {
	return rows[:min(len(rows), maxMarkers)]
}
