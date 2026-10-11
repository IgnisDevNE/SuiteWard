package postgres

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

var _ githubsync.Store = (*Store)(nil)

var errGitHubNotImplemented = errors.New("PostgreSQL GitHub observation store is not implemented")

// Repository implements githubsync.Store.
func (*Store) Repository(context.Context, string) (githubsync.RepositoryState, bool, error) {
	return githubsync.RepositoryState{}, false, errGitHubNotImplemented
}

// RecordCycle implements githubsync.Store.
func (*Store) RecordCycle(context.Context, githubsync.Cycle) error { return errGitHubNotImplemented }

// RecordFailure implements githubsync.Store.
func (*Store) RecordFailure(context.Context, githubsync.Failure) error {
	return errGitHubNotImplemented
}

// Pulls implements githubsync.Store.
func (*Store) Pulls(context.Context, string) ([]githubsync.PullState, error) {
	return nil, errGitHubNotImplemented
}

// Comments implements githubsync.Store.
func (*Store) Comments(context.Context, string, int) ([]githubsync.CommentVersion, error) {
	return nil, errGitHubNotImplemented
}
