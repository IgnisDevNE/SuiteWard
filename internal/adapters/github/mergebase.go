package github

import (
	"context"
	"errors"
)

// MergeBase returns the merge base of the commits baseSHA and headSHA of repo (docs/contracts/github.md, "M2.1: execution reads").
func (r *Reader) MergeBase(ctx context.Context, repo, baseSHA, headSHA string) (string, error) {
	return "", errors.New("github: MergeBase is not implemented")
}
