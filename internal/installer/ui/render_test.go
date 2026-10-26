package ui

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the golden frames in testdata")

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripSGR(s string) string { return sgr.ReplaceAllString(s, "") }

func visibleWidth(row string) int { return utf8.RuneCountInString(stripSGR(row)) }

// goldenText is a frame as the golden files hold it: one row per line, with ESC written as \e so a diff is readable.
func goldenText(f frame) string {
	return strings.ReplaceAll(strings.Join(f.rows, "\n")+"\n", esc, `\e`)
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read the golden frame (regenerate with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs.\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}

// probe hands out its input one byte per read and calls hook before each read: the screen is waiting for the
// terminal then, so the hook sees the frame of an open prompt.
type probe struct {
	r     io.Reader
	reads int
	hook  func(n int)
}

func (p *probe) Read(b []byte) (int, error) {
	p.hook(p.reads)
	p.reads++
	return p.r.Read(b[:1])
}

type stage struct {
	name string
	f    frame
}

// runSequence drives one fixed install through a screen of the given options and returns the frame of four moments (the welcome
// choice with its prompt open, the checks with a spinner, the progress bars, and the summary) and everything written to the terminal.
// A plain screen has no frames, so it returns none.
func runSequence(t *testing.T, o Options) ([]stage, string) {
	t.Helper()
	var (
		s      *Screen
		out    bytes.Buffer
		stages []stage
	)
	capture := func(name string) {
		if s.FullScreen() {
			stages = append(stages, stage{name, snapshot(s)})
		}
	}
	in := &probe{r: strings.NewReader("\n\n")}
	in.hook = func(n int) {
		if names := []string{"welcome", "summary"}; n < len(names) {
			capture(names[n])
		}
	}
	o.Out, o.In, o.Version = &out, in, testVersion
	s, err := Open(o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Steps("Welcome", "Checks", "Database", "Service", "GitHub", "Finish")
	s.Say("Welcome to SuiteWard.\n")
	s.Say("This sets up the service on this PC.\n")
	s.Say("\n")
	if _, err := s.Choose("How should SuiteWard run?", testChoices, 0); err != nil {
		t.Fatalf("Choose: %v", err)
	}
	s.Step(2)
	s.Check("Podman 5.8, rootless")
	s.Check("Ports 8080 and 5432 are free\n")
	s.Warn("Docker is also installed; Podman is used.")
	s.Spin("Waiting for GitHub", 21*time.Second)
	capture("checks")
	s.EndSpin()
	s.Step(3)
	s.Say("Starting the stack.\n")
	s.Say("\n")
	s.Progress("Database", 1, "ready")
	s.Progress("Service", 0.4, "pulling image")
	s.Progress("Total", 0.62, "")
	capture("progress")
	s.Step(6)
	rows := []KV{
		{"App", "suiteward-q (id 123456)"},
		{"Repos", "IgnisDevNE/SuiteWardQ"},
		{"Owner", "octocat (TOTP enrolled)"},
		{"Status URL", "http://127.0.0.1:8080/status"},
		{"Doctor", "12 ok, 1 warning, 0 failed"},
	}
	warnings := []string{"The Podman machine is not started at logon; run `podman machine start` after a restart."}
	if err := s.Summary(rows, warnings); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	mustClose(t, s)
	return stages, out.String()
}

func runStages(t *testing.T, color ColorLevel) []stage {
	t.Helper()
	stages, _ := runSequence(t, fullOptions(color))
	return stages
}

// The same install in plain mode: no escape sequence anywhere, the Say text exactly as written, the steps silent, and one line per
// check, bar and spinner.
func TestPlainModeRunsTheSameSequenceAsPlainText(t *testing.T) {
	stages, out := runSequence(t, plainOptions())
	if len(stages) != 0 {
		t.Fatalf("a plain screen has no frames, got %d", len(stages))
	}
	want := "Welcome to SuiteWard.\n" +
		"This sets up the service on this PC.\n" +
		"\n" +
		"How should SuiteWard run?\n" +
		"1) co-located   alongside the agent, on this PC (default)\n" +
		"2) isolated     on a separate host\n" +
		"Choose [1]: " +
		"Podman 5.8, rootless\n" +
		"Ports 8080 and 5432 are free\n" +
		"Docker is also installed; Podman is used.\n" +
		"Waiting for GitHub...\n" +
		"Starting the stack.\n" +
		"\n" +
		"Database: ready\n" +
		"Service: pulling image\n" +
		"Total: 60%\n" +
		"App: suiteward-q (id 123456)\n" +
		"Repos: IgnisDevNE/SuiteWardQ\n" +
		"Owner: octocat (TOTP enrolled)\n" +
		"Status URL: http://127.0.0.1:8080/status\n" +
		"Doctor: 12 ok, 1 warning, 0 failed\n" +
		"The Podman machine is not started at logon; run `podman machine start` after a restart.\n"
	if out != want {
		t.Fatalf("plain output differs.\ngot:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(out, esc) {
		t.Fatal("plain output holds an escape sequence")
	}
}

func TestGoldenFrames(t *testing.T) {
	for name, color := range map[string]ColorLevel{"off": ColorNone, "truecolor": ColorTrue} {
		for _, st := range runStages(t, color) {
			t.Run(st.name+"/"+name, func(t *testing.T) { checkGolden(t, st.name+"."+name, goldenText(st.f)) })
		}
	}
}

func TestColorOnlyAddsEscapeSequences(t *testing.T) {
	off, on := runStages(t, ColorNone), runStages(t, ColorTrue)
	for i := range off {
		for r := range off[i].f.rows {
			if got := stripSGR(on[i].f.rows[r]); got != off[i].f.rows[r] {
				t.Errorf("%s row %d: color changed the layout\n got %q\nwant %q", off[i].name, r+1, got, off[i].f.rows[r])
			}
		}
		if !strings.Contains(strings.Join(on[i].f.rows, ""), esc+"[") {
			t.Errorf("%s: the truecolor frame holds no color", off[i].name)
		}
	}
	if !strings.Contains(strings.Join(on[0].f.rows, ""), esc+"[38;2;61;214;160m") {
		t.Error("the frame border is not drawn in the brand green")
	}
}

func TestFramesFillTheTerminalExactly(t *testing.T) {
	for _, color := range []ColorLevel{ColorNone, Color256, ColorTrue} {
		for _, st := range runStages(t, color) {
			if len(st.f.rows) != 24 {
				t.Fatalf("%s: %d rows, want 24", st.name, len(st.f.rows))
			}
			for i, row := range st.f.rows {
				if w := visibleWidth(row); w != 80 {
					t.Fatalf("%s row %d is %d columns wide, want 80: %q", st.name, i+1, w, row)
				}
			}
		}
	}
}

func TestStepListMarksProgress(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("Welcome", "Checks", "Database", "Service", "GitHub", "Finish")
	s.Step(4)
	rows := strings.Join(snapshot(s).rows, "\n")
	for _, want := range []string{"✓ Welcome", "✓ Checks", "✓ Database", "● Service", "○ GitHub", "○ Finish", "step 4 of 6"} {
		if !strings.Contains(rows, want) {
			t.Errorf("the frame lacks %q:\n%s", want, rows)
		}
	}
}

func TestStepClearsThePage(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Steps("One", "Two")
	s.Say("old page\n")
	s.Spin("old spinner", 0)
	s.Step(2)
	if rows := strings.Join(snapshot(s).rows, "\n"); strings.Contains(rows, "old") {
		t.Fatalf("a new step must start a clean page:\n%s", rows)
	}
}

func TestSummaryHidesTheStepColumn(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "\n")
	s.Steps("Welcome", "Finish")
	if err := s.Summary([]KV{{"App", "x"}}, nil); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	rows := strings.Join(snapshot(s).rows, "\n")
	if !strings.Contains(rows, "done") || strings.Contains(rows, "Welcome") || strings.Contains(rows, "step 1 of 2") {
		t.Fatalf("the summary screen shows no step column and says done:\n%s", rows)
	}
}

func TestCursorParksAtThePendingLine(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	out.Reset()
	s.Say("prompt: ")
	f := snapshot(s)
	if !f.cursor || f.curRow != 4 || f.curCol != 18+4+len("prompt: ") {
		t.Fatalf("cursor = (%v, row %d, col %d), want a visible cursor at row 4, col 30", f.cursor, f.curRow, f.curCol)
	}
	if want := fmt.Sprintf("%s[%d;%dH%s[?25h", esc, f.curRow, f.curCol, esc); !strings.Contains(out.String(), want) {
		t.Fatalf("the draw did not park and show the cursor (want %q in %q)", want, out.String())
	}
	out.Reset()
	s.Say("\n")
	if f := snapshot(s); f.cursor {
		t.Fatal("the cursor must hide again once the line ends")
	}
	if got := out.String(); !strings.Contains(got, esc+"[?25l") || strings.Contains(got, esc+"[?25h") || strings.Contains(got, "\n") {
		t.Fatalf("a draw without a pending line must hide the cursor and position rows without newlines: %q", got)
	}
}

func TestEveryRowIsPositionedByAbsoluteAddress(t *testing.T) {
	s, out := openScreen(t, fullOptions(ColorNone), "")
	out.Reset()
	s.Say("x\n")
	got := out.String()
	for _, row := range []int{1, 12, 24} {
		if !strings.Contains(got, fmt.Sprintf("%s[%d;1H", esc, row)) {
			t.Errorf("row %d is not addressed", row)
		}
	}
}

func TestAnsweredPromptJoinsItsLine(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "Pedro\n")
	if got, err := s.Ask("Name", "x"); err != nil || got != "Pedro" {
		t.Fatalf("Ask = %q, %v", got, err)
	}
	f := snapshot(s)
	if !strings.Contains(strings.Join(f.rows, "\n"), "Name [x]: Pedro") || f.cursor {
		t.Fatalf("the answer must join its question in the frame and the cursor must hide:\n%s", strings.Join(f.rows, "\n"))
	}
}

func TestSpinnerShowsElapsedTimeAndAFrame(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "⠋ Waiting for GitHub… (00:00)"},
		{100 * time.Millisecond, "⠙ Waiting for GitHub… (00:00)"},
		{21 * time.Second, "⠋ Waiting for GitHub… (00:21)"},
		{21500 * time.Millisecond, "⠴ Waiting for GitHub… (00:21)"},
		{61 * time.Second, "⠋ Waiting for GitHub… (01:01)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			s, _ := openScreen(t, fullOptions(ColorNone), "")
			s.Spin("Waiting for GitHub", tt.elapsed)
			if rows := strings.Join(snapshot(s).rows, "\n"); !strings.Contains(rows, tt.want) {
				t.Fatalf("the frame lacks %q:\n%s", tt.want, rows)
			}
		})
	}
}

func TestSpinnerStaysBelowLaterLinesAndEnds(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Spin("Waiting", 0)
	s.Say("after the spinner started\n")
	rows := strings.Join(snapshot(s).rows, "\n")
	if strings.Index(rows, "after the spinner started") > strings.Index(rows, "Waiting") {
		t.Fatalf("the spinner is a live line at the bottom:\n%s", rows)
	}
	s.EndSpin()
	if rows := strings.Join(snapshot(s).rows, "\n"); strings.Contains(rows, "Waiting") {
		t.Fatalf("EndSpin must remove the spinner:\n%s", rows)
	}
}

func TestProgressBarFillsInCells(t *testing.T) {
	tests := []struct {
		name   string
		f      float64
		filled int
	}{
		{"empty", 0, 0},
		{"half", 0.5, 10},
		{"rounds down", 0.049, 0},
		{"full", 1, 20},
		{"over one is full", 1.5, 20},
		{"negative is empty", -1, 0},
		{"not a number is empty", math.NaN(), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := openScreen(t, fullOptions(ColorNone), "")
			s.Progress("Total", tt.f, "")
			rows := strings.Join(snapshot(s).rows, "\n")
			if strings.Count(rows, "█") != tt.filled || strings.Count(rows, "░") != 20-tt.filled {
				t.Fatalf("want %d filled of 20 cells:\n%s", tt.filled, rows)
			}
		})
	}
}

func TestProgressUpdatesInPlaceAndAlignsLabels(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	s.Progress("A", 0.1, "first")
	s.Progress("Database", 0.5, "pulling")
	s.Progress("A", 0.9, "second")
	rows := strings.Join(snapshot(s).rows, "\n")
	if strings.Count(rows, "first") != 0 || strings.Count(rows, "second") != 1 || strings.Count(rows, "Database") != 1 {
		t.Fatalf("a bar updates in place by its label:\n%s", rows)
	}
	var cols []int
	for _, row := range snapshot(s).rows {
		if i := strings.Index(row, "["); i >= 0 && strings.Contains(row, "░") {
			cols = append(cols, utf8.RuneCountInString(row[:i]))
		}
	}
	if len(cols) != 2 || cols[0] != cols[1] {
		t.Fatalf("the bars of one page start in the same column, got %v", cols)
	}
}

func TestLongTextStaysInsideTheFrame(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorTrue), "")
	s.Steps("A very long step name that cannot fit", "Two")
	s.Say("%s\n", strings.Repeat("word ", 60))
	s.Say("%s\n", strings.Repeat("x", 150))
	s.Check("%s", strings.Repeat("check ", 30))
	s.Warn("%s", strings.Repeat("warn ", 30))
	s.Progress(strings.Repeat("L", 40), 0.5, strings.Repeat("status ", 20))
	s.Spin(strings.Repeat("spin ", 30), time.Minute)
	f := snapshot(s)
	if len(f.rows) != 24 {
		t.Fatalf("%d rows, want 24", len(f.rows))
	}
	for i, row := range f.rows {
		if w := visibleWidth(row); w != 80 {
			t.Fatalf("row %d is %d columns wide, want 80: %q", i+1, w, row)
		}
	}
	if rows := stripSGR(strings.Join(f.rows, "\n")); !strings.Contains(rows, "A very long s…") {
		t.Fatalf("a step name that is too long is cut with an ellipsis:\n%s", rows)
	}
}

func TestOnlyTheLastLinesFit(t *testing.T) {
	s, _ := openScreen(t, fullOptions(ColorNone), "")
	for i := 1; i <= 40; i++ {
		s.Say("L%02d\n", i)
	}
	rows := strings.Join(snapshot(s).rows, "\n")
	// The 18 rows of the body are the notice that rows are hidden and the last 17 lines.
	if !strings.Contains(rows, "L40") || !strings.Contains(rows, "L24") || strings.Contains(rows, "L23") || !strings.Contains(rows, "enlarge the window to read the rest") {
		t.Fatalf("the content column shows the notice and the last 17 lines (L24..L40):\n%s", rows)
	}
}

func TestSmallestTerminalStillFits(t *testing.T) {
	o := fullOptions(ColorNone)
	o.Width, o.Height = 60, 20
	s, _ := openScreen(t, o, "")
	s.Steps("Welcome", "Finish")
	s.Say("%s\n", strings.Repeat("word ", 40))
	s.Progress("Database", 0.5, "pulling the image")
	s.Spin("Waiting", time.Second)
	f := snapshot(s)
	if len(f.rows) != 20 {
		t.Fatalf("%d rows, want 20", len(f.rows))
	}
	for i, row := range f.rows {
		if w := visibleWidth(row); w != 60 {
			t.Fatalf("row %d is %d columns wide, want 60: %q", i+1, w, row)
		}
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"breaks between words", "hello world", 5, []string{"hello", "world"}},
		{"fits on one line", "hello world", 11, []string{"hello world"}},
		{"empty stays one blank line", "", 10, []string{""}},
		{"keeps the indent on every line", "  indented text here", 10, []string{"  indented", "  text", "  here"}},
		{"breaks a word that is too long", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"breaks a Windows path after a backslash", `C:\Users\magal\AppData\Local\suiteward-recovery-kit.txt`, 20, []string{`C:\Users\magal\`, `AppData\Local\`, "suiteward-recovery-k", "it.txt"}},
		{"breaks a path after a slash", "/home/magal/.local/share/suiteward/kit.txt", 18, []string{"/home/magal/", ".local/share/", "suiteward/kit.txt"}},
		{"a word with a separator that does not fit before it is cut", "/abcdefghijklmnop", 6, []string{"/abcde", "fghijk", "lmnop"}},
		{"keeps a trailing space for the cursor", "Choose [1]: ", 57, []string{"Choose [1]: "}},
		{"counts runes, not bytes", "héllo wörld", 5, []string{"héllo", "wörld"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrap(tt.text, tt.width); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wrap(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

func TestFit(t *testing.T) {
	tests := []struct {
		text  string
		width int
		want  string
	}{
		{"abcdef", 4, "abc…"},
		{"abcd", 4, "abcd"},
		{"abc", 4, "abc"},
		{"◆◆◆◆", 3, "◆◆…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	}
	for _, tt := range tests {
		if got := fit(tt.text, tt.width); got != tt.want {
			t.Errorf("fit(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
		}
	}
}
