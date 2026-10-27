package github

import (
	"context"
	"fmt"
)

// MergeBase returns the SHA of the merge base of baseSHA (the tip of a pull request's base branch) and headSHA, which GitHub reports
// in merge_base_commit of the comparison of the two (docs/contracts/github.md, "M2.1: execution reads"). A 404 (no common ancestor, or
// a commit that a force push removed) is rest.ErrNotFound. Both ends are SHAs, so the answer is immutable and skips the ETag cache; one
// commit is listed because only merge_base_commit is read.
func (r *Reader) MergeBase(ctx context.Context, repo, baseSHA, headSHA string) (string, error) {
	if err := checkRepo(repo); err != nil {
		return "", err
	}
	for _, sha := range []string{baseSHA, headSHA} {
		if !hexSHA.MatchString(sha) {
			return "", fmt.Errorf("%w: commit SHA %q is not 40 lowercase hex characters", ErrInvalidArgument, sha)
		}
	}
	path := "/repos/" + repo + "/compare/" + baseSHA + "..." + headSHA + "?per_page=1"
	resp, err := r.client.GetImmutable(ctx, path)
	if err != nil {
		return "", fmt.Errorf("merge base of %s and %s in %s: %w", baseSHA, headSHA, repo, err)
	}
	var raw struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := decode(path, resp.Body, &raw); err != nil {
		return "", err
	}
	if !hexSHA.MatchString(raw.MergeBase.SHA) {
		return "", fmt.Errorf("%w: the answer to %s lacks a 40-hex merge base SHA", ErrInvalidResponse, path)
	}
	return raw.MergeBase.SHA, nil
}
