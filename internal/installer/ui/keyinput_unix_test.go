//go:build linux || darwin

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// A file is not a terminal: asking for per-key input must fail, so that Select asks by number.
func TestEnableKeysRejectsANonTerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "in.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := enableKeys(f); err == nil {
		t.Fatal("enableKeys accepted a file")
	}
}
