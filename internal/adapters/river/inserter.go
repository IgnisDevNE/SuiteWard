// Package river runs background work on River: the job inserter used inside
// governance units of work, the dispatcher worker, the outbox relay and the
// diagnostic probe.
package river

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Inserter inserts jobs through a caller's transaction.
type Inserter struct{}

// NewInserter returns an Inserter whose jobs get maxAttempts attempts.
func NewInserter(maxAttempts int) (*Inserter, error) { return &Inserter{}, nil }

// Insert adds job to tx and returns its id.
func (i *Inserter) Insert(ctx context.Context, tx pgx.Tx, job governance.Job) (int64, error) {
	return 0, errors.New("not implemented")
}
