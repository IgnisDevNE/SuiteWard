package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// checkLedger is checks.Store over the pool of a Store. It is a separate type because Store already has the
// method Current, of owner.Store.
type checkLedger struct{ pool *pgxpool.Pool }

var _ checks.Store = checkLedger{}

// Checks returns the PR check ledger of this database.
func (s *Store) Checks() checks.Store { return checkLedger{pool: s.pool} }

// Current implements checks.Store.
func (l checkLedger) Current(context.Context, int64, int) (checks.Record, bool, error) {
	return checks.Record{}, false, errors.New("postgres: not implemented")
}

// Publish implements checks.Store.
func (l checkLedger) Publish(context.Context, checks.Result, time.Time, func(int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
	return checks.Record{}, false, errors.New("postgres: not implemented")
}
