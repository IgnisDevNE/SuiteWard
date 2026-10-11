package githubsynctest

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

// Memory is an in-memory githubsync.Store.
type Memory struct{}

var _ githubsync.Store = (*Memory)(nil)

var errNotImplemented = errors.New("in-memory GitHub observation store is not implemented")

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{} }

// Repository implements githubsync.Store.
func (*Memory) Repository(context.Context, string) (githubsync.RepositoryState, bool, error) {
	return githubsync.RepositoryState{}, false, errNotImplemented
}

// RecordCycle implements githubsync.Store.
func (*Memory) RecordCycle(context.Context, githubsync.Cycle) error { return errNotImplemented }

// RecordFailure implements githubsync.Store.
func (*Memory) RecordFailure(context.Context, githubsync.Failure) error { return errNotImplemented }

// Pulls implements githubsync.Store.
func (*Memory) Pulls(context.Context, string) ([]githubsync.PullState, error) {
	return nil, errNotImplemented
}

// Comments implements githubsync.Store.
func (*Memory) Comments(context.Context, string, int) ([]githubsync.CommentVersion, error) {
	return nil, errNotImplemented
}
