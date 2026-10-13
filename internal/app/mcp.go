package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/mcp"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

// syncInserter is the part of the job runtime the MCP sync tool uses.
type syncInserter interface {
	InsertSyncRepo(ctx context.Context, repo string) (inserted bool, err error)
}

// observations is the part of the observation store the MCP backend reads.
type observations interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
	Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error)
}

// mcpBackend implements mcp.Backend over the GitHub observations and the job
// inserter. The handler gives it a context without a deadline and logs
// nothing, so every method sets timeout and logs the cause of its own errors.
type mcpBackend struct {
	repos    []string // the configured repositories; none when the GitHub settings are unset
	store    observations
	inserter syncInserter
	now      func() time.Time
	timeout  time.Duration
	logger   *slog.Logger
	tier     string // the configured installation tier
	owners   ownerReader
	ids      *repositoryIDs // learned by the protection check; nil without the GitHub settings
}

// Status reports the configured repositories in configuration order, each
// with its open pulls. Comment bodies are never read.
func (b *mcpBackend) Status(ctx context.Context) (mcp.StatusView, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	enrollment, enrolled, err := b.owners.Current(ctx)
	if err != nil {
		return mcp.StatusView{}, b.failed(ctx, "read the owner", "", err)
	}
	view := mcp.StatusView{GitHubConfigured: len(b.repos) > 0, Repositories: []mcp.RepositoryStatus{}}
	view.Tier, view.TierNote = effectiveTier(b.tier, enrollment, enrolled)
	if enrolled {
		view.Owner = &mcp.OwnerStatus{Login: enrollment.Owner.Login, AccountID: enrollment.Owner.AccountID, EnrolledAt: enrollment.EnrolledAt.UTC()}
	}
	if !view.GitHubConfigured {
		return view, nil
	}
	for _, repo := range b.repos {
		state, _, err := b.store.Repository(ctx, repo)
		if err != nil {
			return mcp.StatusView{}, b.failed(ctx, "read the synchronization state", repo, err)
		}
		pulls, err := b.store.Pulls(ctx, repo)
		if err != nil {
			return mcp.StatusView{}, b.failed(ctx, "read the observed pulls", repo, err)
		}
		entry := mcp.RepositoryStatus{
			Repo: repo, LastSuccess: state.LastSuccess.UTC(), LastError: lastErrorText(state), WaitUntil: state.WaitUntil.UTC(),
			Pulls: []mcp.Pull{},
		}
		for _, pull := range pulls {
			if pull.NotOpenSince.IsZero() { // the closed ones stay in the store as evidence, not in the status
				entry.Pulls = append(entry.Pulls, mcp.Pull{Number: pull.Number, HeadSHA: pull.HeadSHA, BaseRef: pull.BaseRef, UpdatedAt: pull.UpdatedAt.UTC()})
			}
		}
		view.Repositories = append(view.Repositories, entry)
	}
	return view, nil
}

// RequestSync inserts a github_sync_repo job for a configured repository,
// unless the repository is waiting out a rate limit or a job for it is already
// unfinished.
func (b *mcpBackend) RequestSync(ctx context.Context, repo string) (mcp.SyncResult, error) {
	if len(b.repos) == 0 {
		return mcp.SyncResult{}, mcp.ErrNotConfigured
	}
	if !slices.Contains(b.repos, repo) {
		return mcp.SyncResult{}, mcp.ErrUnknownRepository
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	state, found, err := b.store.Repository(ctx, repo)
	if err != nil {
		return mcp.SyncResult{}, b.failed(ctx, "read the synchronization state", repo, err)
	}
	if found && state.WaitUntil.After(b.now()) {
		return mcp.SyncResult{Reason: mcp.ReasonWaiting, WaitUntil: state.WaitUntil.UTC()}, nil
	}
	inserted, err := b.inserter.InsertSyncRepo(ctx, repo)
	if err != nil {
		return mcp.SyncResult{}, b.failed(ctx, "queue the synchronization", repo, err)
	}
	if !inserted {
		return mcp.SyncResult{Reason: mcp.ReasonAlreadyPending}, nil
	}
	return mcp.SyncResult{Queued: true}, nil
}

// failed logs the cause of a failed request and returns it wrapped. The
// handler turns it into a fixed tool error that points to this log line.
func (b *mcpBackend) failed(ctx context.Context, what, repo string, err error) error {
	b.logger.ErrorContext(ctx, "mcp request failed", "action", what, "repo", repo, "error", err)
	if repo == "" {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s of %s: %w", what, repo, err)
}

// failureLog logs every failed cycle the reconciliation records: the
// repository, the classification and the HTTP status, never text GitHub sent.
type failureLog struct {
	githubsync.Store
	logger *slog.Logger
}

func (s failureLog) RecordFailure(ctx context.Context, failure githubsync.Failure) error {
	if err := s.Store.RecordFailure(ctx, failure); err != nil {
		return err
	}
	s.logger.WarnContext(ctx, "github synchronization failed", "repo", failure.Repo, "kind", string(failure.Kind), "status", failure.Status)
	return nil
}
