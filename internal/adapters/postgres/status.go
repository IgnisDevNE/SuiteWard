package postgres

import (
	"context"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// Counts is the number of jobs per River state and of outbox messages per
// delivery state; a state without rows is absent.
type Counts struct {
	Jobs   map[string]int64
	Outbox map[string]int64
}

// SchemaVersion returns the applied schema version, or ErrSchemaNotReady when
// it is not the supported one. A database that cannot be read is a plain error.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	version, err := dbgen.New(s.pool).GetSchemaVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version != migrations.SupportedVersion {
		return 0, fmt.Errorf("%w: schema version %d, this build requires %d", ErrSchemaNotReady, version, migrations.SupportedVersion)
	}
	return version, nil
}

// Counts reports the job and outbox totals the status endpoint serves. The
// outbox relay's own periodic jobs are not counted.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	q := dbgen.New(s.pool)
	jobs, err := q.CountJobsByState(ctx)
	if err != nil {
		return Counts{}, fmt.Errorf("count jobs: %w", err)
	}
	outbox, err := q.CountOutboxByState(ctx)
	if err != nil {
		return Counts{}, fmt.Errorf("count outbox messages: %w", err)
	}
	counts := Counts{Jobs: make(map[string]int64, len(jobs)), Outbox: make(map[string]int64, len(outbox))}
	for _, row := range jobs {
		counts.Jobs[row.State] = row.Total
	}
	for _, row := range outbox {
		counts.Outbox[row.State] = row.Total
	}
	return counts, nil
}
