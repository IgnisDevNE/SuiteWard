package inventory_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// file is one entry of a fake tree; content matters only for a File.
type file struct {
	path    string
	kind    inventory.EntryKind
	content string
}

func regular(path, content string) file { return file{path, inventory.File, content} }
func symlink(path string) file          { return file{path, inventory.Symlink, ""} }
func submodule(path string) file        { return file{path, inventory.Submodule, ""} }

// fakeTree is an in-memory pinned source tree. A Read of a path in
// unreadable, or of a path that is not a File entry, fails the test.
type fakeTree struct {
	t          *testing.T
	files      []file
	unreadable map[string]bool
	entriesErr error
	readErr    error
	reads      []string
}

func newTree(t *testing.T, files ...file) *fakeTree {
	return &fakeTree{t: t, files: files, unreadable: map[string]bool{}}
}

// treeOf builds a fake tree from files keyed by path, in path order.
func treeOf(t *testing.T, files map[string]file) *fakeTree {
	ordered := make([]file, 0, len(files))
	for _, path := range slices.Sorted(maps.Keys(files)) {
		ordered = append(ordered, files[path])
	}
	return newTree(t, ordered...)
}

func (f *fakeTree) Entries(context.Context) ([]inventory.Entry, error) {
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
	entries := make([]inventory.Entry, 0, len(f.files))
	for _, file := range f.files {
		entries = append(entries, inventory.Entry{Path: file.path, Kind: file.kind})
	}
	return entries, nil
}

func (f *fakeTree) Read(_ context.Context, path string) ([]byte, error) {
	f.t.Helper()
	f.reads = append(f.reads, path)
	if f.unreadable[path] {
		f.t.Errorf("read of uncovered path %q", path)
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	for _, file := range f.files {
		if file.path == path && file.kind == inventory.File {
			return []byte(file.content), nil
		}
	}
	f.t.Errorf("read of %q, which is not a file of the tree", path)
	return nil, fmt.Errorf("no file %q", path)
}
