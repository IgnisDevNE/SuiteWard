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

	resolutions  map[int]githubsync.Resolution // the latest resolution of each pull
	processed    map[int]bool
	watermark    time.Time // GitHub's greatest updated_at of the closed listings
	hasWatermark bool      // false until the first RecordClosed
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{repos: map[string]*memoryRepository{}} }

// repository returns the repository, created empty on first use.
func (m *Memory) repository(repo string) *memoryRepository {
	r, ok := m.repos[repo]
	if !ok {
		r = &memoryRepository{pulls: map[int]githubsync.PullState{}, resolutions: map[int]githubsync.Resolution{}, processed: map[int]bool{}}
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
func (m *Memory) RecordCycle(ctx context.Context, cycle githubsync.Cycle) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cycle.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.repos[cycle.Repo]; ok {
		if err := checkCreationTimes(r, cycle); err != nil {
			return nil, err
		}
	}

	r := m.repository(cycle.Repo)
	fresh := freshComments(r, cycle) // before the versions of this cycle are stored
	r.state = githubsync.RepositoryState{Repo: cycle.Repo, InstallationID: cycle.InstallationID, LastSuccess: cycle.At,
		CycleStartedAt: cycle.StartedAt.Truncate(time.Microsecond)} // PostgreSQL keeps microseconds
	recordPulls(r, cycle)
	recordVersions(r, cycle)
	return fresh, nil
}

// checkCreationTimes refuses a cycle that gives a stored comment another creation time, before anything is written.
func checkCreationTimes(r *memoryRepository, cycle githubsync.Cycle) error {
	for _, listing := range cycle.Comments {
		for _, comment := range listing {
			for _, version := range r.versions {
				if version.CommentID == comment.CommentID && !version.CreatedAt.Equal(comment.CreatedAt) {
					return fmt.Errorf("%w: comment %d", githubsync.ErrCreationTimeChanged, comment.CommentID)
				}
			}
		}
	}
	return nil
}

// freshComments returns the sorted ids of the listed comments the repository has no version of yet.
func freshComments(r *memoryRepository, cycle githubsync.Cycle) []int64 {
	stored := map[int64]bool{}
	for _, version := range r.versions {
		stored[version.CommentID] = true
	}
	var fresh []int64
	for _, listing := range cycle.Comments {
		for _, comment := range listing {
			if !stored[comment.CommentID] {
				stored[comment.CommentID] = true
				fresh = append(fresh, comment.CommentID)
			}
		}
	}
	slices.Sort(fresh)
	return fresh
}

// recordPulls refreshes the open pulls and marks the stored pulls the listing no longer has.
func recordPulls(r *memoryRepository, cycle githubsync.Cycle) {
	open := map[int]bool{}
	for _, observed := range cycle.OpenPulls {
		open[observed.Number] = true
		pull, stored := r.pulls[observed.Number]
		if !stored {
			pull = githubsync.PullState{Number: observed.Number, FirstSeen: cycle.At}
		}
		pull.HeadSHA, pull.HeadRef, pull.BaseRef, pull.BaseSHA, pull.UpdatedAt, pull.HeadRepo = observed.HeadSHA, observed.HeadRef, observed.BaseRef, observed.BaseSHA, observed.UpdatedAt, observed.HeadRepo
		pull.LastSeen, pull.NotOpenSince = cycle.At, time.Time{}
		r.pulls[observed.Number] = pull
	}
	for number, pull := range r.pulls {
		if !open[number] && pull.NotOpenSince.IsZero() {
			pull.NotOpenSince = cycle.At
			r.pulls[number] = pull
		}
	}
}

// recordVersions clears the mark of the listed comments, stores the versions it has not seen, and marks the latest version of
// every comment of a listed pull the listing no longer has.
func recordVersions(r *memoryRepository, cycle githubsync.Cycle) {
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
			version := githubsync.CommentVersion{PullNumber: number, CommentID: comment.CommentID, AuthorID: comment.AuthorID, AuthorLogin: comment.AuthorLogin,
				CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, Body: comment.Body, BodyDigest: artifact.Hash([]byte(comment.Body)), FirstSeen: cycle.At}
			stored := slices.ContainsFunc(r.versions, func(v githubsync.CommentVersion) bool {
				return v.CommentID == version.CommentID && v.UpdatedAt.Equal(version.UpdatedAt) && v.BodyDigest == version.BodyDigest
			})
			if !stored {
				r.versions = append(r.versions, version)
			}
		}
	}
	latest := map[int64]githubsync.CommentVersion{}
	for _, v := range r.versions {
		if best, ok := latest[v.CommentID]; !ok || versionOrder(v, best) > 0 {
			latest[v.CommentID] = v
		}
	}
	for i, v := range r.versions {
		if _, listed := cycle.Comments[v.PullNumber]; listed && !seen[v.CommentID] && v.NotSeenSince.IsZero() && versionOrder(v, latest[v.CommentID]) == 0 {
			r.versions[i].NotSeenSince = cycle.At
		}
	}
}

// versionOrder orders the versions of one comment: by GitHub's updated_at, then
// by when they were first seen, then by body digest, so the latest version of
// a comment is deterministic even for two edits within one second.
func versionOrder(a, b githubsync.CommentVersion) int {
	return cmp.Or(a.UpdatedAt.Compare(b.UpdatedAt), a.FirstSeen.Compare(b.FirstSeen), cmp.Compare(a.BodyDigest.String(), b.BodyDigest.String()))
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
	if err := githubsync.ValidatePullNumber(number); err != nil {
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
		return cmp.Or(cmp.Compare(a.CommentID, b.CommentID), versionOrder(a, b))
	})
	return versions, nil
}

// AwaitingResolution implements githubsync.Store.
func (m *Memory) AwaitingResolution(ctx context.Context, repo string) ([]githubsync.PullState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pulls := []githubsync.PullState{}
	if r, ok := m.repos[repo]; ok {
		for number, pull := range r.pulls {
			if resolution, resolved := r.resolutions[number]; !pull.NotOpenSince.IsZero() && (!resolved || !resolution.NotOpenSince.Equal(pull.NotOpenSince)) {
				pulls = append(pulls, pull)
			}
		}
	}
	slices.SortFunc(pulls, func(a, b githubsync.PullState) int { return cmp.Compare(a.Number, b.Number) })
	return pulls, nil
}

// RecordResolution implements githubsync.Store.
func (m *Memory) RecordResolution(ctx context.Context, repo string, resolution githubsync.Resolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := resolution.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var r *memoryRepository
	var pull githubsync.PullState
	ok := false
	if r, ok = m.repos[repo]; ok {
		pull, ok = r.pulls[resolution.Number]
	}
	if !ok || pull.NotOpenSince.IsZero() || !pull.NotOpenSince.Equal(resolution.NotOpenSince) {
		return fmt.Errorf("%w: pull %d of %s is not absent since %v", githubsync.ErrStaleResolution, resolution.Number, repo, resolution.NotOpenSince)
	}
	pull.HeadSHA, pull.HeadRef, pull.BaseRef, pull.BaseSHA, pull.HeadRepo = resolution.HeadSHA, resolution.HeadRef, resolution.BaseRef, resolution.BaseSHA, resolution.HeadRepo
	r.pulls[resolution.Number] = pull
	r.resolutions[resolution.Number] = resolution
	return nil
}

// RecordClosed implements githubsync.Store.
func (m *Memory) RecordClosed(ctx context.Context, repo string, closed []githubsync.ClosedPull, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := githubsync.ValidateClosed(closed, at); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[repo]
	if !ok {
		return fmt.Errorf("%w: repository %s has no recorded cycle", githubsync.ErrInvalidObservation, repo)
	}
	var latest time.Time
	for _, item := range closed {
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
		if _, stored := r.pulls[item.Number]; r.hasWatermark && !stored && !item.MergedAt.IsZero() && !item.UpdatedAt.Before(r.watermark) {
			r.pulls[item.Number] = githubsync.PullState{Number: item.Number, HeadSHA: item.HeadSHA, HeadRef: item.HeadRef, BaseRef: item.BaseRef, BaseSHA: item.BaseSHA,
				UpdatedAt: item.UpdatedAt, FirstSeen: at, LastSeen: at, NotOpenSince: at, HeadRepo: item.HeadRepo}
		}
	}
	if !r.hasWatermark { // the first call only sets the watermark; an empty listing sets the epoch, so the next call is not a first one
		if latest.IsZero() {
			latest = time.Unix(0, 0)
		}
		r.watermark, r.hasWatermark = latest, true
	} else if latest.After(r.watermark) {
		r.watermark = latest
	}
	return nil
}

// Merged implements githubsync.Store.
func (m *Memory) Merged(ctx context.Context, repo string) ([]githubsync.MergedPull, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	merged := []githubsync.MergedPull{}
	if r, ok := m.repos[repo]; ok {
		for number, resolution := range r.resolutions {
			if !resolution.Merged {
				continue
			}
			pull := r.pulls[number]
			// Like the head and base columns, which a resolution refreshes and nothing else touches afterwards.
			resolution.HeadSHA, resolution.HeadRef, resolution.BaseRef, resolution.BaseSHA, resolution.HeadRepo = pull.HeadSHA, pull.HeadRef, pull.BaseRef, pull.BaseSHA, pull.HeadRepo
			merged = append(merged, githubsync.MergedPull{PullState: pull, Resolution: resolution, Processed: r.processed[number]})
		}
	}
	slices.SortFunc(merged, func(a, b githubsync.MergedPull) int {
		return cmp.Or(a.MergedAt.Compare(b.MergedAt), cmp.Compare(a.PullState.Number, b.PullState.Number))
	})
	return merged, nil
}

// MarkProcessed implements githubsync.Store.
func (m *Memory) MarkProcessed(ctx context.Context, repo string, number int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := githubsync.ValidatePullNumber(number); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[repo]
	if !ok {
		return fmt.Errorf("%w: pull %d of %s is not stored", githubsync.ErrInvalidObservation, number, repo)
	}
	if _, stored := r.pulls[number]; !stored {
		return fmt.Errorf("%w: pull %d of %s is not stored", githubsync.ErrInvalidObservation, number, repo)
	}
	r.processed[number] = true
	return nil
}
