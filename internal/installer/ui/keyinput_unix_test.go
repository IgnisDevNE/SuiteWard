//go:build linux || darwin

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// notATerminal is a file: no terminal mode can be asked of it.
func notATerminal(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "in.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

// A file is not a terminal: asking for per-key input must fail, so that Select asks by number.
func TestEnableKeysRejectsANonTerminal(t *testing.T) {
	if _, err := enableKeys(notATerminal(t)); err == nil {
		t.Fatal("enableKeys accepted a file")
	}
}

// A file is not a terminal: its input cannot be flushed, and that is an error, not a silent no-op.
func TestFlushInputRejectsANonTerminal(t *testing.T) {
	if err := flushInput(notATerminal(t)); err == nil {
		t.Fatal("flushInput accepted a file")
	}
}
