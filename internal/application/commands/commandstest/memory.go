package commandstest

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// Memory is an in-memory commands.Store with the semantics of the persistence
// contract. It reads the enrolled projects from an owner.Store and the
// comments from a githubsync.Store, like the PostgreSQL adapter reads their
// tables. One mutex serializes every call, and Record validates and checks
// before its first write, so a failed call changes nothing.
type Memory struct {
	owners       owner.Store
	observations githubsync.Store
	instance     string

	mu     sync.Mutex
	ledger map[entryKey]commands.Entry
	outbox map[string]governance.OutboxMessage
}

type entryKey struct{ repository, comment int64 }

var _ commands.Store = (*Memory)(nil)

// NewMemory returns an empty ledger that reads the enrolled projects from
// owners and the comments from observations. It panics if the system has no
// randomness, which no test can run without.
func NewMemory(owners owner.Store, observations githubsync.Store) *Memory {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	return &Memory{owners: owners, observations: observations, instance: hex.EncodeToString(id),
		ledger: map[entryKey]commands.Entry{}, outbox: map[string]governance.OutboxMessage{}}
}

// Pending implements commands.Store.
func (m *Memory) Pending(ctx context.Context, horizon time.Time, limit int) ([]commands.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := commands.ValidatePending(horizon, limit); err != nil {
		return nil, err
	}
	state, enrolled, err := m.owners.Current(ctx)
	if err != nil {
		return nil, err
	}
	if !enrolled {
		return []commands.Candidate{}, nil // nobody is enrolled: no repository is observed for commands
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := []commands.Candidate{}
	for _, project := range state.Projects {
		pulls, err := m.observations.Pulls(ctx, project.Repo)
		if err != nil {
			return nil, err
		}
		firsts := map[int64]githubsync.CommentVersion{}
		for _, pull := range pulls {
			versions, err := m.observations.Comments(ctx, project.Repo, pull.Number)
			if err != nil {
				return nil, err
			}
			for _, v := range versions {
				if best, ok := firsts[v.CommentID]; !ok || firstOrder(v, best) < 0 {
					firsts[v.CommentID] = v
				}
			}
		}
		for _, v := range firsts {
			if _, recorded := m.ledger[entryKey{project.RepositoryID, v.CommentID}]; !recorded && v.CreatedAt.Before(horizon) {
				pending = append(pending, commands.Candidate{RepositoryID: project.RepositoryID, Repo: project.Repo, PullNumber: v.PullNumber, Comment: v})
			}
		}
	}
	slices.SortFunc(pending, func(a, b commands.Candidate) int {
		return cmp.Or(a.Comment.CreatedAt.Compare(b.Comment.CreatedAt), cmp.Compare(a.Comment.CommentID, b.Comment.CommentID), cmp.Compare(a.RepositoryID, b.RepositoryID))
	})
	return pending[:min(len(pending), limit)], nil
}

// firstOrder orders the versions of one comment by when they were first seen, then by GitHub's updated_at, then by digest.
func firstOrder(a, b githubsync.CommentVersion) int {
	return cmp.Or(a.FirstSeen.Compare(b.FirstSeen), a.UpdatedAt.Compare(b.UpdatedAt), cmp.Compare(a.BodyDigest.String(), b.BodyDigest.String()))
}

// Record implements commands.Store.
func (m *Memory) Record(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	e = e.AtStoredPrecision()
	if ack != nil {
		if err := ack.Validate(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := entryKey{e.RepositoryID, e.CommentID}
	if stored, ok := m.ledger[key]; ok {
		if !sameEntry(stored, e) {
			return fmt.Errorf("%w: comment %d of repository %d", commands.ErrConflict, e.CommentID, e.RepositoryID)
		}
		return nil
	}
	if ack != nil {
		if _, taken := m.outbox[ack.Key]; taken {
			return fmt.Errorf("%w: outbox message %q", governance.ErrOperationConflict, ack.Key)
		}
		m.outbox[ack.Key] = *ack
	}
	m.ledger[key] = e
	return nil
}

func sameEntry(a, b commands.Entry) bool {
	return a.RepositoryID == b.RepositoryID && a.CommentID == b.CommentID && a.PullNumber == b.PullNumber && a.AuthorID == b.AuthorID &&
		a.CreatedAt.Equal(b.CreatedAt) && a.BodyDigest == b.BodyDigest && a.Disposition == b.Disposition && a.Operation == b.Operation && a.DecidedAt.Equal(b.DecidedAt)
}

// LatestOwnerCommand implements commands.Store.
func (m *Memory) LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID int64) (commands.Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return commands.Entry{}, false, err
	}
	if err := commands.ValidateLatest(repositoryID, pull, ownerID); err != nil {
		return commands.Entry{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest commands.Entry
	found := false
	for _, e := range m.ledger {
		if e.RepositoryID == repositoryID && e.PullNumber == pull && e.AuthorID == ownerID && e.Disposition.IsCommand() && (!found || e.CommentID > latest.CommentID) {
			latest, found = e, true
		}
	}
	return latest, found, nil
}

// Skipped implements commands.Store.
func (m *Memory) Skipped(ctx context.Context, repositoryID int64, pull int, since time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := commands.ValidateSkipped(repositoryID, pull); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.ledger {
		if e.RepositoryID == repositoryID && e.PullNumber == pull && e.Disposition == commands.Skipped && !e.CreatedAt.Before(since) {
			return true, nil
		}
	}
	return false, nil
}

// Instance implements commands.Store.
func (m *Memory) Instance(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return m.instance, nil
}

// OutboxKeys returns the key of every committed acknowledgment, sorted.
func (m *Memory) OutboxKeys(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.outbox))
	for key := range m.outbox {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}
