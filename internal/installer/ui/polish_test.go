package ui

import (
	"fmt"
	"strings"
	"testing"
)

// The polish of the screens after the flows were rebuilt: a bullet that wraps, the page of a failure, and a summary that does not fit.

// A bullet is a line that starts with "- ": what it wraps to hangs under its text, not under the dash.
func TestWrapHangsABulletUnderItsText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"a bullet", "- one two three", 8, []string{"- one", "  two", "  three"}},
		{"an indented bullet keeps its indent and hangs two more", "  - alpha beta gamma", 10, []string{"  - alpha", "    beta", "    gamma"}},
		{"a bullet that fits is one line", "  - fits", 20, []string{"  - fits"}},
		{"a dash with no space after it is not a bullet", "-x is a flag", 8, []string{"-x is a", "flag"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrap(tt.text, tt.width); strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("wrap(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

// The warning of the Docker confirmation is a head and two bullets: on the screen a bullet that wraps hangs under its text.
func TestAWarningsBulletsHangUnderTheirText(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Warn("Docker is allowed.\n  - %s\n", strings.Repeat("alpha beta gamma ", 8))
	lines := contentLines(snapshot(s))
	if len(lines) < 4 {
		t.Fatalf("the bullet does not wrap in 80 columns, so there is nothing to align:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[1], "    - alpha") {
		t.Fatalf("the bullet starts %q, want it under the head's text, behind its dash", lines[1])
	}
	for _, l := range lines[2:] {
		if !strings.HasPrefix(l, "      ") || strings.HasPrefix(l, "       ") {
			t.Errorf("a continuation of the bullet is %q, want it six columns in, under the text after the dash", l)
		}
	}
}

// A failure is critical: its title, the mark of the step it stopped in and the mark of its error are in the red of a critical box, and the
// page it stopped on keeps no amber of its own.
func TestAFailureIsDrawnInRed(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "\n")
	s.Steps("Confirm", "Remove")
	s.Say("This is the instance 1a2b3c4d.\n")
	if !s.Fail("the purge was not confirmed") {
		t.Fatal("Fail says that it showed nothing")
	}
	rows := strings.Join(snapshot(s).rows, "\n")
	for name, want := range map[string]string{
		"the title":                  redSGR + "failed" + reset,
		"the mark of the step":       esc + "[1;38;2;242;107;107m! Confirm",
		"the mark of the error line": redSGR + "! " + reset + "the purge was not confirmed",
	} {
		if !strings.Contains(rows, want) {
			t.Errorf("%s is not red: the frame lacks %q:\n%s", name, want, rows)
		}
	}
	if strings.Contains(rows, amberSGR) {
		t.Errorf("the failure page still has amber in it:\n%s", rows)
	}
}

// summaryDemo is a summary of an install with a failing doctor: twelve rows, and a warning for each check that failed.
func summaryDemo() ([]KV, []string) {
	rows := []KV{
		{"Tier", "co-located"}, {"Runtime", "Podman (rootless)"}, {"Instance", "1a2b3c4d"}, {"Release", "v0.2.0"}, {"GitHub App", "1234 on Acme"},
		{"Repositories", "Acme/api, Acme/web"}, {"Owner", "alice (enrolled now)"}, {"Status", "http://127.0.0.1:27081/status"},
		{"Token file", `C:\Users\owner\AppData\Local\SuiteWard\mcp-token`}, {"Settings", `C:\Users\owner\AppData\Local\SuiteWard\settings.yaml`},
		{"Doctor", "3 ok, 0 warnings, 7 failures"}, {"Check again", `suiteward doctor --settings C:\Users\owner\AppData\Local\SuiteWard\settings.yaml`},
	}
	var warns []string
	for i := 1; i <= 7; i++ {
		warns = append(warns, fmt.Sprintf("FAIL check%d: this check found something that needs a look, and what to run to fix it: run the fix of check%d and then check again", i, i))
	}
	return rows, warns
}

// summaryLines is the body of a summary frame, which has no step column: one entry per row, without trailing spaces or the blank rows at the end.
func summaryLines(f frame) []string {
	var lines []string
	for _, r := range f.rows[3 : len(f.rows)-3] {
		cells := []rune(r)
		lines = append(lines, strings.TrimRight(string(cells[2:len(cells)-2]), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// A summary that is taller than the window keeps what says how the install ended, and trims its warnings from the end, behind one row that
// says so. Close still prints all of them.
func TestASummaryTallerThanTheWindowTrimsItsWarningsFromTheEnd(t *testing.T) {
	for _, w := range sizes {
		t.Run(w.name, func(t *testing.T) {
			s, out := openScreen(t, windowOptions(w.width, w.height), "\n")
			rows, warns := summaryDemo()
			if err := s.Summary(rows, warns); err != nil {
				t.Fatal(err)
			}
			lines := summaryLines(snapshot(s))
			text := strings.Join(lines, "\n")
			if len(lines) > w.height-frameRows {
				t.Fatalf("%d rows in a body of %d:\n%s", len(lines), w.height-frameRows, text)
			}
			for _, want := range []string{"Tier  ", "Instance  ", "Status  ", "Doctor  "} {
				if !strings.Contains(text, want) {
					t.Errorf("the summary lost its %q row:\n%s", strings.TrimSpace(want), text)
				}
			}
			if strings.Count(text, trimRow) != 1 {
				t.Errorf("want exactly one row that says the page was trimmed (%q):\n%s", trimRow, text)
			}
			if strings.Contains(text, "check7") {
				t.Errorf("the last warning is still on the screen, so nothing was trimmed from the end:\n%s", text)
			}
			mustClose(t, s)
			if printed := afterLastLeave(out.String()); !strings.Contains(printed, "check1") || !strings.Contains(printed, "check7") || !strings.Contains(printed, "Settings:") {
				t.Errorf("Close does not print everything the window could not show:\n%s", printed)
			}
		})
	}
}

// On the smallest window even the rows of a summary do not all fit: the ones that say what runs and where it answers stay, the others go
// from the end, behind the same row.
func TestASummaryWhoseRowsDoNotFitKeepsTheKeyRows(t *testing.T) {
	s, _ := openScreen(t, windowOptions(60, 20), "\n")
	rows, warns := summaryDemo()
	if err := s.Summary(rows, warns); err != nil {
		t.Fatal(err)
	}
	lines := summaryLines(snapshot(s))
	text := strings.Join(lines, "\n")
	if len(lines) > 14 {
		t.Fatalf("%d rows in a body of 14:\n%s", len(lines), text)
	}
	for _, want := range []string{"Tier  ", "Instance  ", "Status  ", "Doctor  ", trimRow} {
		if !strings.Contains(text, want) {
			t.Errorf("the summary lost %q:\n%s", strings.TrimSpace(want), text)
		}
	}
}
