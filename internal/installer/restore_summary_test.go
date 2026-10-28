package installer

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// pageText is the content of the last frame drawn in out as one line of words, whatever its layout: a summary has no step column, and a
// sentence that wrapped reads as it was written.
func pageText(t *testing.T, out string, w, h int) string {
	t.Helper()
	start := strings.LastIndex(out, esc+"[1;1H")
	if start < 0 {
		t.Fatal("no frame was drawn")
	}
	rows := rowAddress.Split(out[start:], -1)[1:]
	if len(rows) < h {
		t.Fatalf("the frame has %d rows, want %d", len(rows), h)
	}
	var words []string
	for _, r := range rows[3 : h-3] {
		words = append(words, strings.Fields(strings.Trim(r, "│ "))...)
	}
	return strings.Join(words, " ")
}

// What a restore leaves behind is on the page the person reads at the end, the summary, and not only in the scrollback that follows it: the
// restored canonical can be behind the default branch, and one corrective pull request brings it level.
func TestTheSummaryPageOfARestoreSaysHowToBringTheCanonicalLevel(t *testing.T) {
	run, _ := restoreOnScreen(t, termWidth, termHeight, config.TierCoLocated)
	out := run.output()
	page := pageText(t, out[:strings.LastIndex(out, leaveAlt)], termWidth, termHeight)
	for _, want := range []string{"Restored", "may be behind the default branch on GitHub", "one small corrective pull request", "Details: docs/backup.md"} {
		if !strings.Contains(page, want) {
			t.Errorf("the summary page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, trimmed) {
		t.Errorf("the summary page is trimmed at %dx%d:\n%s", termWidth, termHeight, page)
	}
	if n := strings.Count(run.console.tail(), "may be behind the default branch on GitHub"); n != 1 {
		t.Errorf("the scrollback keeps the line %d times, want once:\n%s", n, run.console.tail())
	}
}
