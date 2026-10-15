package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// integrityLedger is integrity.Store over the pool of a Store. It is a separate type because Store already has the
// method Record, of commands.Store.
type integrityLedger struct{ pool *pgxpool.Pool }

var _ integrity.Store = integrityLedger{}

// Integrity returns the canonical integrity verdicts of this database.
func (s *Store) Integrity() integrity.Store { return integrityLedger{pool: s.pool} }

// Record implements integrity.Store. One statement replaces the verdict and keeps a failed one against an
// unavailable one of the same version, so no transaction is needed. An unknown version fails the foreign key.
func (l integrityLedger) Record(ctx context.Context, r integrity.Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	err := dbgen.New(l.pool).UpsertCanonicalIntegrity(ctx, dbgen.UpsertCanonicalIntegrityParams{ProjectID: string(r.Project), SuiteID: string(r.Suite),
		VersionID: string(r.Version), Verdict: string(r.Verdict), CheckedAt: pgtype.Timestamptz{Time: r.CheckedAt, Valid: true}})
	if err != nil {
		return fmt.Errorf("record the integrity verdict: %w", err)
	}
	return nil
}

// Latest implements integrity.Store.
func (l integrityLedger) Latest(ctx context.Context, project contract.ProjectID, suite contract.SuiteID) (integrity.Record, bool, error) {
	row, err := dbgen.New(l.pool).GetCanonicalIntegrity(ctx, dbgen.GetCanonicalIntegrityParams{ProjectID: string(project), SuiteID: string(suite)})
	if errors.Is(err, pgx.ErrNoRows) {
		return integrity.Record{}, false, nil
	}
	if err != nil {
		return integrity.Record{}, false, fmt.Errorf("read the integrity verdict: %w", err)
	}
	return integrity.Record{Project: contract.ProjectID(row.ProjectID), Suite: contract.SuiteID(row.SuiteID), Version: contract.SuiteVersionID(row.VersionID),
		Verdict: integrity.Verdict(row.Verdict), CheckedAt: observedTime(row.CheckedAt)}, true, nil
}
