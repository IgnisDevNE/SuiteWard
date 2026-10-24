//go:build windows

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// notAConsole is a file: no console mode can be asked of it.
func notAConsole(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
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

// A file is not a console: asking for virtual-terminal processing must fail, so that OpenOS falls back to plain output.
func TestEnableVTRejectsANonConsole(t *testing.T) {
	if _, err := enableVT(notAConsole(t)); err == nil {
		t.Fatal("enableVT accepted a file")
	}
}

// The same for per-key input: Select then asks by number.
func TestEnableKeysRejectsANonConsole(t *testing.T) {
	if _, err := enableKeys(notAConsole(t)); err == nil {
		t.Fatal("enableKeys accepted a file")
	}
}
