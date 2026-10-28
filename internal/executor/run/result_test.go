package run

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestACompletionAtTheBoundsOfTheWireIsAccepted(t *testing.T) {
	c := completion{
		tests:      make([]testRow, maxTests),
		markers:    markerRows(maxMarkers),
		submodules: make([]string, maxSubmodules),
		unparsed:   make([]string, maxUnparsed),
	}
	if err := c.check(); err != nil {
		t.Errorf("check = %v; the bounds themselves are allowed", err)
	}
}

func TestACompletionPastABoundIsTooLarge(t *testing.T) {
	for name, c := range map[string]completion{
		"too many tests":      {tests: make([]testRow, maxTests+1)},
		"too many submodules": {submodules: make([]string, maxSubmodules+1)},
		"too many unparsed":   {unparsed: make([]string, maxUnparsed+1)},
	} {
		err := c.check()
		var reason *reasonError
		if !errors.As(err, &reason) || reason.reason != reasonTooLarge {
			t.Errorf("%s: check = %v; want too-large", name, err)
		}
	}
}

func TestMarkersAreCutToTheirBoundNotRefused(t *testing.T) {
	var rows []markerRow
	for i := range maxMarkers + 50 {
		rows = append(rows, markerRow{Kind: "env-read", Path: fmt.Sprintf("f%03d.go", i), Line: 1})
	}
	got := capMarkers(rows)
	if len(got) != maxMarkers || got[0].Path != "f000.go" || !strings.HasPrefix(got[len(got)-1].Path, "f199") {
		t.Errorf("capMarkers kept %d markers, from %v to %v; want the first %d", len(got), got[0], got[len(got)-1], maxMarkers)
	}
	if few := capMarkers(rows[:3]); len(few) != 3 {
		t.Errorf("capMarkers cut a short list to %d", len(few))
	}
}

// markerRows are n markers of the first line of a file.
func markerRows(n int) []markerRow {
	rows := make([]markerRow, n)
	for i := range rows {
		rows[i] = markerRow{Kind: "env-read", Path: "a.go", Line: 1}
	}
	return rows
}
