package github

import (
	"context"
	"errors"
)

// TreeEntry is one blob or submodule of a commit's tree, as the executor API's lease lists it: the path, the git mode and the git id.
type TreeEntry struct{ Path, Mode, SHA string }

// TreeEntries reads the entries of the commit's tree.
func (r *Reader) TreeEntries(_ context.Context, _, _ string) ([]TreeEntry, error) {
	return nil, errors.New("not implemented")
}
