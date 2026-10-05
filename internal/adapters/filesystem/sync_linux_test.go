//go:build linux

package filesystem

import (
	"os"
	"testing"
)

func TestSyncDirectorySyncsOpenRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := syncDirectory(root); err != nil {
		t.Fatalf("sync of an existing directory: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(root); err == nil {
		t.Fatal("sync of a closed root succeeded; errors must be returned")
	}
}
