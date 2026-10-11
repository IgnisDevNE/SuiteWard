package governance_test

import (
	"context"
	"fmt"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// treeFile is one entry of a fake tree; content matters only for a File.
type treeFile struct {
	path    string
	kind    inventory.EntryKind
	content string
}

// fakeTree is an in-memory pinned source tree. From its second read on, a
// path listed in changed returns that content instead, as a tree that moved
// between the inventory and the content store would.
type fakeTree struct {
	files   []treeFile
	changed map[string]string
	reads   map[string]int
}

func newFakeTree(files ...treeFile) *fakeTree {
	return &fakeTree{files: files, changed: map[string]string{}, reads: map[string]int{}}
}

func (f *fakeTree) Entries(context.Context) ([]inventory.Entry, error) {
	entries := make([]inventory.Entry, 0, len(f.files))
	for _, file := range f.files {
		entries = append(entries, inventory.Entry{Path: file.path, Kind: file.kind})
	}
	return entries, nil
}

func (f *fakeTree) Read(_ context.Context, path string) ([]byte, error) {
	f.reads[path]++
	if later, moved := f.changed[path]; moved && f.reads[path] > 1 {
		return []byte(later), nil
	}
	for _, file := range f.files {
		if file.path == path && file.kind == inventory.File {
			return []byte(file.content), nil
		}
	}
	return nil, fmt.Errorf("no file %q", path)
}

// fakeWriter is an in-memory ContentWriter that does not verify digests, so a
// test sees exactly which digests the use case chose to store.
type fakeWriter struct {
	stored map[artifact.Digest]string
	fail   error
}

func newFakeWriter() *fakeWriter { return &fakeWriter{stored: map[artifact.Digest]string{}} }

func (w *fakeWriter) Put(_ context.Context, digest artifact.Digest, content io.Reader) error {
	if w.fail != nil {
		return w.fail
	}
	bytes, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	w.stored[digest] = string(bytes)
	return nil
}
