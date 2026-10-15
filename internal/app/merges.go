package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// mergeReader is the part of the GitHub reader the merges job uses.
type mergeReader interface {
	Pull(ctx context.Context, repo string, number int) (github.PullDetail, error)
	ClosedPulls(ctx context.Context, repo string) ([]github.PullDetail, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// mergeStore is the part of the observation store the merges job uses.
type mergeStore interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
	AwaitingResolution(ctx context.Context, repo string) ([]githubsync.PullState, error)
	RecordResolution(ctx context.Context, repo string, r githubsync.Resolution) error
	RecordClosed(ctx context.Context, repo string, closed []githubsync.ClosedPull, at time.Time) error
	Merged(ctx context.Context, repo string) ([]githubsync.MergedPull, error)
	MarkProcessed(ctx context.Context, repo string, number int) error
}

// mergesJob is the github_merges job.
type mergesJob struct {
	repos     []string // the configured repositories
	reader    mergeReader
	owners    owner.Store
	suites    governance.UnitOfWork
	content   governance.ContentWriter
	store     mergeStore
	integrity integrity.Store
	tier      string             // the configured INSTALLATION_TIER
	problems  *bootstrapProblems // shown in /status (github[].merges)
	logger    *slog.Logger
	now       func() time.Time
}

func (j *mergesJob) run(ctx context.Context) error { return nil }
