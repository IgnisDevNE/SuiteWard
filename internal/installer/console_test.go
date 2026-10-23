package installer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// spinWriter is a terminal that counts the frames that show the spinner of label, and says when the second one was drawn.
type spinWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	label  string
	frames int
	second chan struct{}
}

func (w *spinWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(p, []byte(w.label)) {
		if w.frames++; w.frames == 2 {
			close(w.second)
		}
	}
	return w.buf.Write(p)
}

func (w *spinWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// The spinner of a running command moves until the command returns, its goroutine is gone when working returns, and the row is
// removed: no tick may draw it again afterwards.
func TestWorkingMovesTheSpinnerUntilTheCommandReturns(t *testing.T) {
	old := spinTick
	spinTick = time.Millisecond
	t.Cleanup(func() { spinTick = old })
	w := &spinWriter{label: "Pulling suiteward", second: make(chan struct{})}
	scr, err := ui.Open(ui.Options{Out: w, In: strings.NewReader(""), Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("the pull failed")
	got := working(scr, "Pulling suiteward", func() error {
		<-w.second // the spinner has been drawn twice: it moves while the command runs
		return failure
	})
	if !errors.Is(got, failure) {
		t.Errorf("working returned %v, want the command's error", got)
	}
	before := w.String()
	if frame := before[strings.LastIndex(before, esc+"[1;1H"):]; strings.Contains(frame, "Pulling suiteward") {
		t.Errorf("the last frame still shows the spinner:\n%s", frame)
	}
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingOnAPlainScreenOnlyRunsTheCommand(t *testing.T) {
	var out bytes.Buffer
	scr, err := ui.Open(ui.Options{Out: &out, In: strings.NewReader(""), Mode: ui.ModePlain})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := working(scr, "Pulling suiteward", func() error { ran = true; return nil }); err != nil || !ran || out.Len() != 0 {
		t.Errorf("working = %v, ran %v, printed %q", err, ran, out.String())
	}
	barOn(scr, "Database").set(0.5, "starting") // a plain screen has no bar, and a wait that has none draws none
	if out.Len() != 0 {
		t.Errorf("a plain run printed %q", out.String())
	}
}

// Ctrl-C at the summary only skips the wait for Enter: the work is done, and the summary is kept for Close all the same.
func TestShowSummaryEndsWithTheContextWithoutAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scr, _, out := fullScreenOn(t, ctx, strings.NewReader(""))
	if err := showSummary(scr, func() []ui.KV { return []ui.KV{{Key: "Status", Value: "http://127.0.0.1:8081/status"}} }); err != nil {
		t.Fatalf("showSummary = %v", err)
	}
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String()[strings.LastIndex(out.String(), leaveAlt):], "Status: http://127.0.0.1:8081/status") {
		t.Error("the summary is not in the scrollback after Close")
	}
}
