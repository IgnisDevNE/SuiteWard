package installer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// fullScreenOn opens a full screen over a buffer, reading answers from the same bufio.Reader the Resolver gets, as a command does.
func fullScreenOn(t *testing.T, ctx context.Context, in io.Reader) (*ui.Screen, *bufio.Reader, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	br := bufio.NewReader(in)
	s, err := ui.Open(ui.Options{Out: &out, In: br, Context: ctx, Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: 100, Height: 30})
	if err != nil {
		t.Fatal(err)
	}
	return s, br, &out
}

// lastFrame is the part of out the screen drew last.
func lastFrame(out string) string { return out[strings.LastIndex(out, esc+"[1;1H"):] }

// A question goes through the Screen, so that the answer joins it on the page: a terminal echoes what is typed at the cursor, which
// the frame does not have.
func TestResolverQuestionsAreAnsweredOnThePageOfAFullScreen(t *testing.T) {
	scr, br, out := fullScreenOn(t, context.Background(), strings.NewReader("9000\ny\nenroll\n"))
	term := Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return []byte("pw"), nil }}
	r := newTestResolver(t, nil, nil, term)

	if got, err := r.Value("http-port", "HTTP port on 127.0.0.1", "8081"); err != nil || got != "9000" {
		t.Fatalf("Value = %q, %v", got, err)
	}
	if ok, err := r.confirm("yes", "Remove the containers?"); err != nil || !ok {
		t.Fatalf("confirm = %v, %v", ok, err)
	}
	if line, err := r.ask("Type \"enroll\": "); err != nil || line != "enroll" {
		t.Fatalf("ask = %q, %v", line, err)
	}
	for _, want := range []string{"HTTP port on 127.0.0.1 [8081]: 9000", "Remove the containers? [y/N]: y", `Type "enroll": enroll`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the page lacks the answered question %q", want)
		}
	}
}

// A hidden read cannot be interrupted, so the frame must not offer Ctrl-C while it is open.
func TestResolverSecretIsReadThroughTheScreen(t *testing.T) {
	scr, br, out := fullScreenOn(t, context.Background(), strings.NewReader(""))
	var frame string
	term := Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) {
		frame = lastFrame(out.String())
		return []byte("pw"), nil
	}}
	r := newTestResolver(t, nil, nil, term)
	got, err := r.Secret("app-key", "GitHub App private key (PEM)")
	if err != nil || string(got) != "pw" {
		t.Fatalf("Secret = %q, %v", got, err)
	}
	if !strings.Contains(frame, "input is hidden") || !strings.Contains(frame, "Enter to confirm") || strings.Contains(frame, "Ctrl-C") {
		t.Errorf("the frame while the secret is read:\n%s", frame)
	}
	if strings.Contains(out.String(), "pw") {
		t.Error("the secret was drawn")
	}
}

// A done context ends the question before anything is read: the abandoned read would share the buffer with the next one.
func TestResolverQuestionsEndWithTheContextAndReadNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	questions := map[string]func(r *Resolver) error{
		"Value":   func(r *Resolver) error { _, err := r.Value("tier", "Tier", "co-located"); return err },
		"Int":     func(r *Resolver) error { _, err := r.Int("http-port", "HTTP port", 8081); return err },
		"confirm": func(r *Resolver) error { _, err := r.confirm("yes", "Go on?"); return err },
		"ask":     func(r *Resolver) error { _, err := r.ask("Type enroll: "); return err },
	}
	for name, ask := range questions {
		t.Run(name, func(t *testing.T) {
			in := &countingReader{data: "answer\n"}
			scr, br, _ := fullScreenOn(t, ctx, in)
			r := newTestResolver(t, nil, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }})
			if err := ask(r); !errors.Is(err, context.Canceled) {
				t.Errorf("got %v, want the context's error", err)
			}
			if in.reads != 0 {
				t.Errorf("the input was read %d times after the context was done", in.reads)
			}
		})
	}
}
