package postgres

import (
	"context"
	"errors"
)

// Counts is the number of jobs per River state and of outbox messages per
// delivery state; a state without rows is absent.
type Counts struct {
	Jobs   map[string]int64
	Outbox map[string]int64
}

// SchemaVersion returns the applied schema version, or ErrSchemaNotReady when
// it cannot be read or is not the supported one.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	return 0, errors.New("not implemented")
}

// Counts reports the job and outbox totals the status endpoint serves. The
// outbox relay's own periodic jobs are not counted.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	return Counts{}, errors.New("not implemented")
}
