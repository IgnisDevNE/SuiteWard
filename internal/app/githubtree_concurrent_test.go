package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// safeTree is a tree whose reads may overlap when it says so.
type safeTree struct{ concurrent bool }

func (safeTree) Entries(context.Context) ([]inventory.Entry, error) { return nil, nil }
func (safeTree) Read(_ context.Context, path string) ([]byte, error) {
	return []byte("content of " + path), nil
}
func (s safeTree) ConcurrentReads() bool { return s.concurrent }

// The inventory overlaps the reads of a tree that allows it, so the tree the checks job reads through must serve them at once, and
// must not allow them when the tree it wraps does not.
func TestGitHubTreeServesConcurrentReadsOfATreeThatAllowsThem(t *testing.T) {
	tree := &githubTree{tree: safeTree{concurrent: true}, files: map[string][]byte{}}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			path := fmt.Sprintf("tests/f%d_test.go", i)
			if got, err := tree.Read(t.Context(), path); err != nil || string(got) != "content of "+path {
				t.Errorf("Read(%s) = %q, %v", path, got, err)
			}
		})
	}
	wg.Wait()

	concurrent := func(tree inventory.Tree) bool {
		c, ok := tree.(interface{ ConcurrentReads() bool })
		return ok && c.ConcurrentReads()
	}
	if !concurrent(tree) {
		t.Error("a tree over one that allows concurrent reads does not allow them")
	}
	if concurrent(&githubTree{tree: safeTree{}, files: map[string][]byte{}}) {
		t.Error("a tree over one that does not allow concurrent reads allows them")
	}
}
