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
	r.CheckedAt = r.CheckedAt.UTC().Truncate(time.Microsecond) // all PostgreSQL stores
	m.records[key] = integrity.Next(current, found, r)
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
