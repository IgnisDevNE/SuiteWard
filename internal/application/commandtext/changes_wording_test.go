package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
)

// removalChange removes a test file and another file: on a reduction of the protected scope they only leave the protected files.
func removalChange() []report.FileChange {
	return []report.FileChange{
		{Path: "tests/old_test.go", Status: report.Removed, Old: []byte("package a\n")},
		{Path: "config/other.yml", Status: report.Removed, Old: []byte("package a\n")},
		{Path: "tests/new_test.go", Status: report.Added, New: []byte("package a\n")},
	}
}

// A revision that reduces the protected scope removes files from it that may still exist: "removed" must not read as deleted.
func TestRevisionCommentOfAReductionSaysThatTheFilesWereRemovedFromTheProtectedFiles(t *testing.T) {
	r := revisionWith(t, commandtext.Isolated, removalChange()...)
	r.Reduced, r.ReducedRules, r.ReducedPaths, r.ReducedPathsTotal = true, []string{"exclude /tests/old_test.go"}, []string{"tests/old_test.go"}, 1

	body := revisionText(t, r)

	for _, want := range []string{"- Files: 1 added, 0 modified, 2 removed from the protected files\n", "- Marker lines seen: 1 test file removed from the protected files\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "delet") {
		t.Errorf("body says deleted for a file that left the protected files:\n%s", body)
	}
}

func TestRevisionCommentOfAnotherChangeKeepsItsWording(t *testing.T) {
	body := revisionText(t, revisionWith(t, commandtext.Isolated, removalChange()...))

	for _, want := range []string{"- Files: 1 added, 0 modified, 2 removed\n", "- Marker lines seen: 1 removed test file\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "from the protected files") || strings.Contains(strings.ToLower(body), "delet") {
		t.Errorf("body words a removal as a reduction or as a deletion:\n%s", body)
	}
}

// A check summary is public and shows marker counts: it says that they are not a verdict, once, whatever they are.
func TestCheckSummaryThatShowsMarkerLinesSaysThatTheyAreNotAVerdict(t *testing.T) {
	const note = "Marker lines are lines to look at, not a verdict."
	for name, changes := range map[string][]report.FileChange{
		"with markers": smallChange(), "without markers": {{Path: "tests/a.go", Status: report.Added, New: []byte("x\n")}},
	} {
		t.Run(name, func(t *testing.T) {
			c := check(commandtext.CheckInProgress, "approval-missing")
			summary := summaryOf(t, changes...)
			c.Changes = &summary
			_, text := checkText(t, c)
			if strings.Count(text, note) != 1 {
				t.Errorf("the summary has the note %d times, want once:\n%s", strings.Count(text, note), text)
			}
			if strings.Index(text, note) < strings.Index(text, "Marker lines seen") {
				t.Errorf("the note comes before the marker lines it is about:\n%s", text)
			}
		})
	}
	_, plain := checkText(t, check(commandtext.CheckInProgress, "approval-missing"))
	if strings.Contains(plain, "not a verdict") {
		t.Errorf("a summary that shows no marker lines has the note:\n%s", plain)
	}
}
