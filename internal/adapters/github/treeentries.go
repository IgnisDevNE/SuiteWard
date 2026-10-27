package github

import (
	"context"
	"fmt"
)

// TreeEntry is one blob or submodule of a commit's tree, as the executor API's lease lists it: the path, the git mode and the git id.
type TreeEntry struct{ Path, Mode, SHA string }

// entryTypes pairs each git mode that a tree entry may have with the object type GitHub names for it. 100664 is git's legacy mode of a regular file.
// A directory (040000, tree) is not an entry here.
var entryTypes = map[string]string{"100644": "blob", "100664": "blob", "100755": "blob", "120000": "blob", "160000": "commit"}

// TreeEntries reads the entries of the commit's tree: its blobs and its submodules, each with its git mode and git id (docs/contracts/github.md,
// "M2.1: execution reads"). It is the read of Tree without the inventory model: one request of an immutable path through the budget, and
// the same errors. A tree that GitHub truncated is ErrTreeTruncated and never a partial list; an entry that git does not list is
// ErrInvalidResponse, and its error does not quote what GitHub sent.
func (r *Reader) TreeEntries(ctx context.Context, repo, commitSHA string) ([]TreeEntry, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if !hexSHA.MatchString(commitSHA) {
		return nil, fmt.Errorf("%w: commit SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, commitSHA)
	}
	path := "/repos/" + repo + "/git/trees/" + commitSHA + "?recursive=1"
	resp, err := r.client.GetImmutable(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("tree entries of %s at %s: %w", repo, commitSHA, err)
	}
	var answer struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := decode(path, resp.Body, &answer); err != nil {
		return nil, err
	}
	switch {
	case answer.Tree == nil: // a missing or null list decodes to nil; an empty tree is []
		return nil, fmt.Errorf("%w: the answer to %s lists no tree", ErrInvalidResponse, path)
	case answer.Truncated:
		return nil, fmt.Errorf("%w: GitHub truncated the tree of %s at %s", ErrTreeTruncated, repo, commitSHA)
	}
	entries := make([]TreeEntry, 0, len(answer.Tree))
	for i, e := range answer.Tree {
		if e.Mode == "040000" && e.Type == "tree" {
			continue
		}
		if entryTypes[e.Mode] != e.Type || e.Path == "" || !hexSHA.MatchString(e.SHA) {
			return nil, fmt.Errorf("%w: entry %d of the tree of %s at %s is no blob or submodule that git lists", ErrInvalidResponse, i, repo, commitSHA)
		}
		entries = append(entries, TreeEntry{Path: e.Path, Mode: e.Mode, SHA: e.SHA})
	}
	return entries, nil
}
