//go:build windows

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// A file is not a console: asking for virtual-terminal processing must fail, so that OpenOS falls back to plain output.
func TestEnableVTRejectsANonConsole(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := enableVT(f); err == nil {
		t.Fatal("enableVT accepted a file")
	}
}
