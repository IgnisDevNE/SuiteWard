package commandtext

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

// maxNotShown bounds the files a comment names among the ones without a line-by-line diff; with the changed files
// (maxListed) and maxShown it keeps a comment far below GitHub's limit of 65536 characters.
const maxNotShown = 10

// Changes is what a change report found, as the numbers and names a revision comment and a check summary state. It
// says what was seen, never that it is right or wrong. Added, Modified and Removed count every changed file. Counted
// is how many of them have line counts, and LinesAdded and LinesRemoved cover those files only. Top lists at most 20
// of the Counted files, the most changed lines first and then by path. NotShown lists at most 10 of the NotShownTotal
// files that have no line-by-line diff: a binary file, a file too large or too different to compare, one the
// comparison work ran out before, or one whose diff the report left out to keep its size. Truncated says that the
// report left out some details, as report.Report.Truncated does.
type Changes struct {
	Added, Modified, Removed int
	Counted                  int
	LinesAdded, LinesRemoved int
	Top                      []ChangedFile
	NotShown                 []NotShown
	NotShownTotal            int
	Markers                  Markers
	Truncated                bool
	// Reduced says that the revision reduces the protected scope, so that a removed file left the protected files and may still exist.
	// RevisionComment sets it from Revision.Reduced; ChangesOf does not know it.
	Reduced bool
}

// ChangedFile is a counted file with its lines. Status is added, modified or removed.
type ChangedFile struct {
	Path, Status             string
	LinesAdded, LinesRemoved int
}

// NotShown is a file that has no line-by-line diff in the report, with the reason ChangesOf gives.
type NotShown struct {
	Path, Why string
}

// Markers count the marker lines of a report by kind.
type Markers struct {
	RemovedAssertions, AddedSkips, Focused, DeletedTests, TestFilesRemoved int
}

// ChangesOf summarizes r: the counts of its totals, and the files to name.
func ChangesOf(r report.Report) Changes {
	m := r.Totals.Markers
	c := Changes{
		Added: r.Totals.Files[report.Added], Modified: r.Totals.Files[report.Modified], Removed: r.Totals.Files[report.Removed],
		LinesAdded: r.Totals.LinesAdded, LinesRemoved: r.Totals.LinesRemoved, Truncated: r.Truncated,
		Markers: Markers{RemovedAssertions: m[report.MarkRemovedAssertion], AddedSkips: m[report.MarkAddedSkip], Focused: m[report.MarkFocus],
			DeletedTests: m[report.MarkDeletedTest], TestFilesRemoved: m[report.MarkTestFileRemoved]},
	}
	var withCounts []ChangedFile
	for _, f := range r.Files { // in path order
		if f.CountsKnown {
			withCounts = append(withCounts, ChangedFile{Path: f.Path, Status: string(f.Status), LinesAdded: f.LinesAdded, LinesRemoved: f.LinesRemoved})
		}
		if why := notShownWhy(f); why != "" {
			c.NotShownTotal++
			if len(c.NotShown) < maxNotShown {
				c.NotShown = append(c.NotShown, NotShown{Path: f.Path, Why: why})
			}
		}
	}
	c.Counted = len(withCounts)
	slices.SortStableFunc(withCounts, func(a, b ChangedFile) int { return b.LinesAdded + b.LinesRemoved - a.LinesAdded - a.LinesRemoved }) // stable: path order breaks ties
	c.Top = withCounts[:min(len(withCounts), maxListed)]
	return c
}

// notShownWhy says why f has no line-by-line diff in the report, or "" when it has one.
func notShownWhy(f report.File) string {
	switch {
	case f.Limit == report.LimitBinary:
		return "binary file"
	case f.Limit == report.LimitSize:
		return "too large to compare"
	case f.Limit == report.LimitEditDistance:
		return "too different to compare"
	case f.Limit == report.LimitBudget:
		return "not compared (work budget ran out)"
	case f.Truncated:
		return "diff left out (report size budget)"
	}
	return ""
}

// validateChanges reports why c cannot be stated.
func validateChanges(c Changes) error {
	files := c.Added + c.Modified + c.Removed
	switch {
	case min(c.Added, c.Modified, c.Removed, c.Counted, c.LinesAdded, c.LinesRemoved, c.NotShownTotal) < 0,
		min(c.Markers.RemovedAssertions, c.Markers.AddedSkips, c.Markers.Focused, c.Markers.DeletedTests, c.Markers.TestFilesRemoved) < 0:
		return errors.New("commandtext: a negative count in the changes")
	case c.Counted > files:
		return fmt.Errorf("commandtext: %d counted files of %d changed files", c.Counted, files)
	case len(c.Top) > maxListed || len(c.Top) > c.Counted:
		return fmt.Errorf("commandtext: %d changed files listed, of %d counted, at most %d", len(c.Top), c.Counted, maxListed)
	case len(c.NotShown) > maxNotShown || len(c.NotShown) > c.NotShownTotal:
		return fmt.Errorf("commandtext: %d files not shown listed, of %d, at most %d", len(c.NotShown), c.NotShownTotal, maxNotShown)
	}
	for _, f := range c.Top {
		if f.Path == "" || (f.Status != string(report.Added) && f.Status != string(report.Modified) && f.Status != string(report.Removed)) || min(f.LinesAdded, f.LinesRemoved) < 0 {
			return errors.New("commandtext: a changed file without a path or a known status, or with negative lines")
		}
	}
	for _, f := range c.NotShown {
		if f.Path == "" || f.Why == "" {
			return errors.New("commandtext: a file not shown without a path or a reason")
		}
	}
	return nil
}

// Facts are the lines that state the numbers of c: the files, the lines, the files not shown line by line when there are some, and the
// marker lines seen. listed says that the reader finds the files not shown named right below.
func (c Changes) Facts(listed bool) []string {
	removed := "removed"
	if c.Reduced { // a removed file left the protected files, and may still exist
		removed += " from the protected files"
	}
	facts := []string{
		fmt.Sprintf("Files: %d added, %d modified, %d %s", c.Added, c.Modified, c.Removed, removed),
		fmt.Sprintf("Lines: +%d/−%d (counted in %d of %d files)", c.LinesAdded, c.LinesRemoved, c.Counted, c.Added+c.Modified+c.Removed),
	}
	if c.NotShownTotal > 0 {
		fact := "Not shown line by line: " + counted(c.NotShownTotal, "file", "files")
		if listed {
			fact += " (listed below)"
		}
		facts = append(facts, fact)
	}
	return append(facts, "Marker lines seen: "+markerPhrases(c.Markers, c.Reduced))
}

// changeFacts are the bullets that state the numbers of c.
func changeFacts(c Changes, listed bool) []string {
	facts := c.Facts(listed)
	for i, fact := range facts {
		facts[i] = "- " + fact
	}
	return facts
}

func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// markerPhrases lists the marker lines of m in a fixed order, or says that there are none. On a reduction a test file removed left the protected
// files, and does not say deleted.
func markerPhrases(m Markers, reduced bool) string {
	testFile, testFiles := "removed test file", "removed test files"
	if reduced {
		testFile, testFiles = "test file removed from the protected files", "test files removed from the protected files"
	}
	var phrases []string
	for _, p := range []struct {
		n         int
		one, many string
	}{
		{m.RemovedAssertions, "removed assertion line", "removed assertion lines"},
		{m.AddedSkips, "added skip line", "added skip lines"},
		{m.Focused, "added focus line", "added focus lines"},
		{m.DeletedTests, "removed test declaration", "removed test declarations"},
		{m.TestFilesRemoved, testFile, testFiles},
	} {
		if p.n > 0 {
			phrases = append(phrases, counted(p.n, p.one, p.many))
		}
	}
	if len(phrases) == 0 {
		return "none"
	}
	return strings.Join(phrases, ", ")
}

// reportLinkPattern is the link to the report of a revision: a loopback address, as the settings allow, the repository and the reference.
var reportLinkPattern = regexp.MustCompile(`^http://(?:127\.0\.0\.1|localhost):[0-9]{1,5}/reports/[A-Za-z0-9._%-]+/[A-Za-z0-9._%-]+/(P[0-9]+-R[0-9]+)$`)

// changesSection renders what the comment states about the change of the files: the numbers, the files to name and the link to the full
// report, or the listings of paths when there are no numbers.
func changesSection(r Revision) []string {
	if r.Changes == nil {
		// The listings of a revision without numbers read as they did in M1.8, byte for byte, a reduction included.
		parts := []string{listing("Protected files added", r.Added, r.AddedTotal), listing("Protected files modified", r.Modified, r.ModifiedTotal), listing("Protected files removed", r.Removed, r.RemovedTotal)}
		if r.ChangesUnavailable {
			parts = append(parts, "SuiteWard could not read the files of this revision to count the lines that changed, so the numbers are unknown.")
		}
		return parts
	}
	c := *r.Changes
	c.Reduced = r.Reduced
	parts := []string{
		"**What changed in the protected files.** SuiteWard counts the changed lines and points out some of them as marker lines; it does not judge them.",
		strings.Join(changeFacts(c, true), "\n"),
	}
	if len(c.Top) > 0 {
		title := "Changed files, most changed lines first"
		if c.Counted > len(c.Top) {
			title += fmt.Sprintf(" (%d of %d)", len(c.Top), c.Counted)
		}
		parts = append(parts, title+":\n\n"+fencedRows(topRows(c.Top))+andMore(c.Counted-len(c.Top)))
	}
	if len(c.NotShown) > 0 {
		parts = append(parts, "Files without a line-by-line diff:\n\n"+fencedRows(notShownRows(c.NotShown))+andMore(c.NotShownTotal-len(c.NotShown)))
	}
	if c.Truncated {
		parts = append(parts, "Some details of the full report were left out to keep it small.")
	}
	if r.ReportURL != "" {
		parts = append(parts, "[Full change report]("+r.ReportURL+")",
			"This link opens on the machine that runs this SuiteWard instance; on the isolated tier, through a port forward of the HTTP port. Ctrl+click (or middle-click) the link to open it in a new tab.")
	}
	return parts
}

func andMore(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\nand %d more", n)
}

// topRows are the rows of the changed files: their lines, their status and their path.
func topRows(files []ChangedFile) [][]string {
	rows := make([][]string, len(files))
	for i, f := range files {
		rows[i] = []string{fmt.Sprintf("+%d/−%d", f.LinesAdded, f.LinesRemoved), f.Status, display(f.Path)}
	}
	return rows
}

func notShownRows(files []NotShown) [][]string {
	rows := make([][]string, len(files))
	for i, f := range files {
		rows[i] = []string{f.Why, display(f.Path)}
	}
	return rows
}

// fencedRows shows rows in a fence, each cell but the last padded to the widest of its column and two spaces between the cells.
func fencedRows(rows [][]string) string {
	width := make([]int, len(rows[0])-1)
	for _, row := range rows {
		for i := range width {
			width[i] = max(width[i], utf8.RuneCountInString(row[i]))
		}
	}
	lines := make([]string, len(rows))
	for i, row := range rows {
		var line strings.Builder
		for j, cell := range row {
			line.WriteString(cell)
			if j < len(width) {
				line.WriteString(strings.Repeat(" ", width[j]-utf8.RuneCountInString(cell)+2))
			}
		}
		lines[i] = line.String()
	}
	return fenced(strings.Join(lines, "\n") + "\n")
}

// validateReport reports why the numbers and the link of r cannot be stated.
func validateReport(r Revision) error {
	switch {
	case r.Changes != nil && r.ChangesUnavailable:
		return errors.New("commandtext: changes that are also unavailable")
	case r.ReportURL != "" && r.Changes == nil:
		return errors.New("commandtext: a report link without the numbers of the change")
	case r.Changes != nil:
		if err := validateChanges(*r.Changes); err != nil {
			return err
		}
	}
	if r.ReportURL == "" {
		return nil
	}
	ref, err := reference(r.Proposal, r.Revision)
	if m := reportLinkPattern.FindStringSubmatch(r.ReportURL); err != nil || m == nil || m[1] != ref {
		return errors.New("commandtext: the report link is not the link of a loopback report of this revision")
	}
	return nil
}

// Visible returns s with every character that hides itself written as an escape: a control character, a bidirectional or
// zero-width one, and a byte that is not UTF-8. A literal backslash is written twice, so that an escape is never the spelling of a file's own text.
// A tab stays, for the source lines a report page shows.
func Visible(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\t' || !hidden(r):
			b.WriteRune(r)
		default:
			quoted := strconv.QuoteRuneToASCII(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		}
		i += size
	}
	return b.String()
}
