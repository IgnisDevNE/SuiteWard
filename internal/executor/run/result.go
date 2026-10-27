package run

import "errors"

// The bounds of a result (runtime contract, "POST /jobs/{id}/result").
const (
	maxTests      = 10000
	maxMarkers    = 200
	maxSubmodules = 100
	maxUnparsed   = 100
	maxBodyBytes  = 4 << 20
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

// check refuses what the wire cannot carry: more tests, submodules or unparsed files than its bounds. The markers are cut to their bound
// before, as a list of lines to read.
func (c completion) check() error {
	return errors.New("not implemented")
}

// capMarkers keeps the first maxMarkers lines: they are lines to read, never a verdict.
func capMarkers(rows []markerRow) []markerRow {
	return rows
}
