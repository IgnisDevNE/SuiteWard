package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOSEnvReadsTheVariablesThatAreSet(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("WT_SESSION", "x")
	if err := os.Unsetenv("WT_SESSION"); err != nil {
		t.Fatal(err)
	}
	env := OSEnv(false)
	if v, set := env.Vars["NO_COLOR"]; !set || v != "" {
		t.Errorf("NO_COLOR set but empty must be recorded as set, got %q, %v", v, set)
	}
	if env.Vars["TERM"] != "xterm" || env.Vars["COLORTERM"] != "truecolor" {
		t.Errorf("Vars = %v", env.Vars)
	}
	if _, set := env.Vars["WT_SESSION"]; set {
		t.Error("an unset variable must not be recorded")
	}
	if env.Mode() != ModePlain {
		t.Error("NO_COLOR must select plain output")
	}
}

func TestOSEnvPlainFlagWins(t *testing.T) {
	if got := OSEnv(true).Mode(); got != ModePlain {
		t.Fatalf("Mode() = %v with the plain flag", got)
	}
}

// OpenOS with the plain flag never touches the console, so this is safe to run in a terminal.
func TestOpenOSPlain(t *testing.T) {
	s, err := OpenOS(context.Background(), strings.NewReader(""), testVersion, true)
	if err != nil {
		t.Fatalf("OpenOS: %v", err)
	}
	if s.FullScreen() {
		t.Fatal("the plain flag opened a full screen")
	}
	mustClose(t, s)
}

// OpenOS gives the screen no flush that is known to fail: where the input cannot be flushed (a file here, a console handle opened for reading
// only on Windows), FlushInput is left nil, as InputReady is where the input cannot be waited on.
func TestInputFlusherIsNilForAnInputThatCannotBeFlushed(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "in.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if inputFlusher(f) != nil {
		t.Error("a flush was offered for a file, which cannot be flushed")
	}
}
