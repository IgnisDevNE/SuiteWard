// Package checkstest provides an in-memory checks.Store and the conformance
// suite that every checks.Store must pass.
package checkstest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Memory is an in-memory checks.Store with the semantics of the persistence contract. One mutex serializes
// every call, and Publish checks everything before its first write, so a failed call changes nothing.
type Memory struct {
	mu      sync.Mutex
	records map[pull]stored
	outbox  map[string]outboxEntry // key -> the message
}

type pull struct {
	repository int64
	number     int
}

// outboxEntry is an outbox message: failed says it ended failed, at finishedAt.
type outboxEntry struct {
	failed     bool
	finishedAt time.Time
}

// stored is a record with the key of its current generation's message.
type stored struct {
	record     checks.Record
	messageKey string
}

var _ checks.Store = (*Memory)(nil)

// NewMemory returns an empty ledger.
func NewMemory() *Memory {
	return &Memory{records: map[pull]stored{}, outbox: map[string]outboxEntry{}}
}

// Current implements checks.Store.
func (m *Memory) Current(ctx context.Context, repositoryID int64, number int) (checks.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return checks.Record{}, false, err
	}
	if err := checks.ValidatePull(repositoryID, number); err != nil {
		return checks.Record{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, found := m.records[pull{repositoryID, number}]
	return s.record, found, nil
}

// Publish implements checks.Store.
func (m *Memory) Publish(ctx context.Context, r checks.Result, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return checks.Record{}, false, err
	}
	if err := checks.ValidatePublish(r, at); err != nil {
		return checks.Record{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := pull{r.RepositoryID, r.PullNumber}
	current, found := m.records[key]
	next, advance := checks.Next(current.record, found, r, at, found && m.outbox[current.messageKey].failed)
	messageKey := current.messageKey
	if advance {
		msg, err := message(next.Generation)
		if err != nil {
			return checks.Record{}, false, fmt.Errorf("build the check message: %w", err)
		}
		if err := msg.Validate(); err != nil {
			return checks.Record{}, false, err
		}
		if _, taken := m.outbox[msg.Key]; taken {
			return checks.Record{}, false, fmt.Errorf("%w: outbox message %q", governance.ErrOperationConflict, msg.Key)
		}
		m.outbox[msg.Key] = outboxEntry{}
		messageKey = msg.Key
	}
	m.records[key] = stored{record: next, messageKey: messageKey}
	return next, advance, nil
}

// OutboxKeys returns the key of every committed message, sorted.
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

// FailMessage marks a message as failed long ago, as the relay does once its attempts are used up: any
// cooldown after it has passed.
func (m *Memory) FailMessage(ctx context.Context, key string) error {
	return m.FailMessageAt(ctx, key, time.Unix(0, 0))
}

// FailMessageAt marks a message as failed at finishedAt.
func (m *Memory) FailMessageAt(ctx context.Context, key string, finishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.outbox[key]; !ok {
		return fmt.Errorf("%w: outbox message %q", governance.ErrNotFound, key)
	}
	m.outbox[key] = outboxEntry{failed: true, finishedAt: finishedAt}
	return nil
}
