package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/mcp"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

var errNotImplemented = errors.New("not implemented")

// syncInserter is the part of the job runtime the MCP sync tool uses.
type syncInserter interface {
	InsertSyncRepo(ctx context.Context, repo string) (inserted bool, err error)
}

// observations is the part of the observation store the MCP backend reads.
type observations interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
	Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error)
}

// mcpBackend implements mcp.Backend over the GitHub observations and the job inserter.
type mcpBackend struct {
	repos    []string // the configured repositories; none when the GitHub settings are unset
	store    observations
	inserter syncInserter
	now      func() time.Time
	timeout  time.Duration
	logger   *slog.Logger
}

func (b *mcpBackend) Status(ctx context.Context) (mcp.StatusView, error) {
	return mcp.StatusView{}, errNotImplemented
}

func (b *mcpBackend) RequestSync(ctx context.Context, repo string) (mcp.SyncResult, error) {
	return mcp.SyncResult{}, errNotImplemented
}

// failureLog logs every failed cycle the reconciliation records.
type failureLog struct {
	githubsync.Store
	logger *slog.Logger
}
