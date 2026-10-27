package app

import (
	"context"
	"fmt"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// githubTrees reads the tree entries of a commit with the synchronization reader (its token reads the contents), as the entries of the lease answer.
func githubTrees(reader *github.Reader) treeReader {
	return func(ctx context.Context, repo, commitSHA string) ([]treeEntry, error) {
		entries, err := reader.TreeEntries(ctx, repo, commitSHA)
		if err != nil {
			return nil, err
		}
		listed := make([]treeEntry, len(entries))
		for i, e := range entries {
			listed[i] = treeEntry{Path: e.Path, Mode: e.Mode, SHA: e.SHA}
		}
		return listed, nil
	}
}

// errNoGitHub is what the readers of a service without the GitHub settings fail with: a GitHub failure, which consumes no attempt and ends a job on the
// GitHub bound once EXECUTOR_GITHUB_WAIT has passed. The API is served whenever the token is set, so such a service still answers its leases.
var errNoGitHub = fmt.Errorf("%w: the GitHub settings are not set", execution.ErrGitHubUnavailable)

func noTrees(context.Context, string, string) ([]treeEntry, error) { return nil, errNoGitHub }

func noArchives(context.Context, string, string, io.Writer, int64) (int64, error) {
	return 0, errNoGitHub
}
