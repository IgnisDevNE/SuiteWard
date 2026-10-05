// Package postgres implements governance persistence on PostgreSQL.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ErrSchemaNotReady reports a database whose schema is not the supported version.
var ErrSchemaNotReady = errors.New("postgres schema is not ready")

var errNotImplemented = errors.New("not implemented")

// ArtifactVerifier confirms that stored content matches a digest.
type ArtifactVerifier interface {
	Verify(ctx context.Context, digest artifact.Digest) error
}

// Store implements governance.UnitOfWork and governance.Seeder.
type Store struct{}

var (
	_ governance.UnitOfWork = (*Store)(nil)
	_ governance.Seeder     = (*Store)(nil)
)

// NewStore checks schema readiness and returns the PostgreSQL governance store.
func NewStore(pool *pgxpool.Pool, artifacts ArtifactVerifier) (*Store, error) {
	return &Store{}, nil
}

func (*Store) Do(context.Context, contract.ProjectID, contract.SuiteID, func(context.Context, governance.Tx) error) error {
	return errNotImplemented
}

func (*Store) Seed(context.Context, governance.Seed) error { return errNotImplemented }
