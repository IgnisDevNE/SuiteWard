package githubsynctest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// Memory is an in-memory githubsync.Store with the semantics of the
// persistence contract. One mutex serializes every call, so a cycle never
// sees another half done; a cycle that fails validation or the created_at
// check changes nothing because both run before the first write.
type Memory struct {
	mu    sync.Mutex
	repos map[string]*memoryRepository
}

var _ githubsync.Store = (*Memory)(nil)

type memoryRepository struct {
	state    githubsync.RepositoryState
	pulls    map[int]githubsync.PullState
	versions []githubsync.CommentVersion
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{repos: map[string]*memoryRepository{}} }

// repository returns the repository, created empty on first use.
func (m *Memory) repository(repo string) *memoryRepository {
	r, ok := m.repos[repo]
	if !ok {
		r = &memoryRepository{pulls: map[int]githubsync.PullState{}}
		m.repos[repo] = r
	}
	return r
}

// Repository implements githubsync.Store.
func (m *Memory) Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error) {
	if err := ctx.Err(); err != nil {
		return githubsync.RepositoryState{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[repo]
	if !ok {
		return githubsync.RepositoryState{}, false, nil
	}
	return r.state, true, nil
}

// RecordCycle implements githubsync.Store.
func (m *Memory) RecordCycle(ctx context.Context, cycle githubsync.Cycle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cycle.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.repos[cycle.Repo]; ok {
		for _, listing := range cycle.Comments {
			for _, comment := range listing {
				for _, version := range r.versions {
					if version.CommentID == comment.CommentID && !version.CreatedAt.Equal(comment.CreatedAt) {
						return fmt.Errorf("GitHub comment %d changed its creation time", comment.CommentID)
					}
				}
			}
		}
	}

	r := m.repository(cycle.Repo)
	r.state = githubsync.RepositoryState{Repo: cycle.Repo, InstallationID: cycle.InstallationID, LastSuccess: cycle.At}

	open := map[int]bool{}
	for _, observed := range cycle.OpenPulls {
		open[observed.Number] = true
		pull, known := r.pulls[observed.Number]
		if !known {
			pull = githubsync.PullState{Number: observed.Number, FirstSeen: cycle.At}
		}
		pull.HeadSHA, pull.HeadRef, pull.BaseRef, pull.BaseSHA, pull.UpdatedAt = observed.HeadSHA, observed.HeadRef, observed.BaseRef, observed.BaseSHA, observed.UpdatedAt
		pull.LastSeen, pull.NotOpenSince = cycle.At, time.Time{}
		r.pulls[observed.Number] = pull
	}
	for number, pull := range r.pulls {
		if !open[number] && pull.NotOpenSince.IsZero() {
			pull.NotOpenSince = cycle.At
			r.pulls[number] = pull
		}
	}

	seen := map[int64]bool{}
	for _, listing := range cycle.Comments {
		for _, comment := range listing {
			seen[comment.CommentID] = true
		}
	}
	for i := range r.versions {
		if seen[r.versions[i].CommentID] {
			r.versions[i].NotSeenSince = time.Time{}
		}
	}
	for number, listing := range cycle.Comments {
		for _, comment := range listing {
			known := slices.ContainsFunc(r.versions, func(v githubsync.CommentVersion) bool {
				return v.CommentID == comment.CommentID && v.UpdatedAt.Equal(comment.UpdatedAt)
			})
			if !known {
				r.versions = append(r.versions, githubsync.CommentVersion{PullNumber: number, CommentID: comment.CommentID, AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin,
					CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, Body: comment.Body, BodyDigest: artifact.Hash([]byte(comment.Body)), FirstSeen: cycle.At})
			}
		}
	}
	latest := map[int64]time.Time{}
	for _, v := range r.versions {
		if v.UpdatedAt.After(latest[v.CommentID]) {
			latest[v.CommentID] = v.UpdatedAt
		}
	}
	for i, v := range r.versions {
		if _, listed := cycle.Comments[v.PullNumber]; listed && !seen[v.CommentID] && v.NotSeenSince.IsZero() && v.UpdatedAt.Equal(latest[v.CommentID]) {
			r.versions[i].NotSeenSince = cycle.At
		}
	}
	return nil
}

// RecordFailure implements githubsync.Store.
func (m *Memory) RecordFailure(ctx context.Context, failure githubsync.Failure) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := failure.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := &m.repository(failure.Repo).state
	state.Repo, state.InstallationID = failure.Repo, failure.InstallationID
	state.LastError, state.LastStatus, state.WaitUntil = failure.Kind, failure.Status, failure.WaitUntil
	return nil
}

// Pulls implements githubsync.Store.
func (m *Memory) Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pulls := []githubsync.PullState{}
	if r, ok := m.repos[repo]; ok {
		for _, pull := range r.pulls {
			pulls = append(pulls, pull)
		}
	}
	slices.SortFunc(pulls, func(a, b githubsync.PullState) int { return cmp.Compare(a.Number, b.Number) })
	return pulls, nil
}

// Comments implements githubsync.Store.
func (m *Memory) Comments(ctx context.Context, repo string, number int) ([]githubsync.CommentVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := []githubsync.CommentVersion{}
	if r, ok := m.repos[repo]; ok {
		for _, v := range r.versions {
			if v.PullNumber == number {
				versions = append(versions, v)
			}
		}
	}
	slices.SortFunc(versions, func(a, b githubsync.CommentVersion) int {
		return cmp.Or(cmp.Compare(a.CommentID, b.CommentID), a.UpdatedAt.Compare(b.UpdatedAt))
	})
	return versions, nil
}
