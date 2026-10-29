package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

const crlfNote = "This file uses Windows line endings (CRLF). They are not shown on each line."

func crlfReport(files ...report.File) report.Report {
	return report.Report{Files: files, Totals: report.Totals{Files: map[report.Status]int{report.Modified: len(files)}, Markers: map[report.MarkerKind]int{}}}
}

func hunkOf(lines ...report.Line) []report.Hunk {
	return []report.Hunk{{OldStart: 1, OldLines: len(lines), NewStart: 1, NewLines: len(lines), Lines: lines}}
}

func TestTheReportPageDropsTheCarriageReturnOfTheLinesOfACRLFFileAndSaysSoOnce(t *testing.T) {
	windows := report.File{Path: "win.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 1, LinesRemoved: 1, Hunks: hunkOf(
		report.Line{Kind: report.LineContext, Text: "same\r"},
		report.Line{Kind: report.LineRemoved, Text: "old\r"},
		report.Line{Kind: report.LineAdded, Text: "new\rwith a stray one\r"},
		report.Line{Kind: report.LineAdded, Text: "last line has no terminator", NoNewline: true},
	)}
	unix := report.File{Path: "unix.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 1, LinesRemoved: 1, Hunks: hunkOf(
		report.Line{Kind: report.LineContext, Text: "same"},
		report.Line{Kind: report.LineRemoved, Text: "old"},
		report.Line{Kind: report.LineAdded, Text: "new"},
	)}
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: crlfReport(unix, windows)}})

	response := getReport(a, "127.0.0.1:8080", reportPath)

	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Count(body, crlfNote) != 1 {
		t.Fatalf("GET = %d with the CRLF note %d times, want 200 and once, for win.txt only:\n%s", response.Code, strings.Count(body, crlfNote), body)
	}
	if strings.Index(body, crlfNote) < strings.Index(body, "win.txt") {
		t.Errorf("the note comes before the file that uses CRLF:\n%s", body)
	}
	if strings.Count(body, `\r`) != 1 { // the stray one in the middle of a line stays visible; the ones that end a line are dropped
		t.Errorf("the page shows %d carriage returns as escapes, want only the stray one:\n%s", strings.Count(body, `\r`), body)
	}
	if !strings.Contains(body, "&#43;new\\rwith a stray one</span>") {
		t.Errorf("the added line is not shown as new, a visible stray return and the rest, with its ending dropped:\n%s", body)
	}
}

// Without a majority of lines that end with one, a carriage return is not a line ending: the page shows it.
func TestTheReportPageShowsTheCarriageReturnsOfAFileThatDoesNotUseCRLF(t *testing.T) {
	mixed := report.File{Path: "mixed.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 2, Hunks: hunkOf(
		report.Line{Kind: report.LineContext, Text: "plain"},
		report.Line{Kind: report.LineContext, Text: "plain too"},
		report.Line{Kind: report.LineAdded, Text: "one with a return\r"},
		report.Line{Kind: report.LineAdded, Text: "plain again"},
	)}
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: crlfReport(mixed)}})

	body := getReport(a, "127.0.0.1:8080", reportPath).Body.String()

	if strings.Contains(body, crlfNote) || strings.Count(body, `\r`) != 1 {
		t.Fatalf("a file with one line of four ending in a return is a CRLF file (the note %d times, %d escapes):\n%s", strings.Count(body, crlfNote), strings.Count(body, `\r`), body)
	}
}
