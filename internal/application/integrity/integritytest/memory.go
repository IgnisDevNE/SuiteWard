// Package integritytest provides an in-memory integrity.Store and the conformance suite that every
// integrity.Store must pass.
package integritytest

import (
	"context"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Memory is an in-memory integrity.Store with the semantics of the persistence contract. One mutex serializes
// every call.
type Memory struct {
	mu      sync.Mutex
	records map[suite]integrity.Record
}

type suite struct {
	project contract.ProjectID
	suite   contract.SuiteID
}

var _ integrity.Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{records: map[suite]integrity.Record{}} }

// Record implements integrity.Store.
func (m *Memory) Record(ctx context.Context, r integrity.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := suite{r.Project, r.Suite}
	current, found := m.records[key]
	m.records[key] = next(current, found, r)
	return nil
}

// Latest implements integrity.Store.
func (m *Memory) Latest(ctx context.Context, project contract.ProjectID, id contract.SuiteID) (integrity.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return integrity.Record{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, found := m.records[suite{project, id}]
	return r, found, nil
}

// next is the record kept when r is recorded over current (found false: there is none): r, except that "unavailable" never
// replaces "failed" for the same version, where only CheckedAt moves. The PostgreSQL store writes the same rule as one
// statement, so that concurrent writers cannot lose a confirmed failure; the conformance suite pins both. Times are kept at
// microseconds, all PostgreSQL stores.
func next(current integrity.Record, found bool, r integrity.Record) integrity.Record {
	r.CheckedAt = r.CheckedAt.UTC().Truncate(time.Microsecond)
	if found && current.Version == r.Version && current.Verdict == integrity.Failed && r.Verdict == integrity.Unavailable {
		current.CheckedAt = r.CheckedAt
		return current
	}
	return r
}
