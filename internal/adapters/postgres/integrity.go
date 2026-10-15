package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// integrityLedger is integrity.Store over the pool of a Store. It is a separate type because Store already has the
// method Record, of commands.Store.
type integrityLedger struct{ pool *pgxpool.Pool }

var _ integrity.Store = integrityLedger{}

// Integrity returns the canonical integrity verdicts of this database.
func (s *Store) Integrity() integrity.Store { return integrityLedger{pool: s.pool} }

// Record implements integrity.Store.
func (l integrityLedger) Record(context.Context, integrity.Record) error {
	return errors.New("postgres: integrity Record is not implemented")
}

// Latest implements integrity.Store.
func (l integrityLedger) Latest(context.Context, contract.ProjectID, contract.SuiteID) (integrity.Record, bool, error) {
	return integrity.Record{}, false, errors.New("postgres: integrity Latest is not implemented")
}
