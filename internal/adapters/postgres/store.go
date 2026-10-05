// Package postgres implements governance persistence on PostgreSQL: one
// transaction per unit of work, with the Suite row locked for its duration.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ErrSchemaNotReady reports a database whose schema is not the supported version.
var ErrSchemaNotReady = errors.New("postgres schema is not ready")

const (
	schemaCheckTimeout = 10 * time.Second
	rollbackTimeout    = 10 * time.Second
)

// ArtifactVerifier confirms that stored content is available and matches its
// digest. *filesystem.Store satisfies it.
type ArtifactVerifier interface {
	Verify(ctx context.Context, digest artifact.Digest) error
}

// Store implements governance.UnitOfWork and governance.Seeder on PostgreSQL.
type Store struct {
	pool      *pgxpool.Pool
	artifacts ArtifactVerifier
}

var (
	_ governance.UnitOfWork = (*Store)(nil)
	_ governance.Seeder     = (*Store)(nil)
)

// NewStore returns a Store after checking that the database is at the schema
// version this build supports. The artifact verifier is consulted only when a
// version is written.
func NewStore(pool *pgxpool.Pool, artifacts ArtifactVerifier) (*Store, error) {
	if pool == nil || artifacts == nil {
		return nil, errors.New("postgres store requires a connection pool and an artifact verifier")
	}
	ctx, cancel := context.WithTimeout(context.Background(), schemaCheckTimeout)
	defer cancel()
	version, err := dbgen.New(pool).GetSchemaVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: read schema version: %w", ErrSchemaNotReady, err)
	}
	if version != migrations.SupportedVersion {
		return nil, fmt.Errorf("%w: schema version %d, this build requires %d", ErrSchemaNotReady, version, migrations.SupportedVersion)
	}
	return &Store{pool: pool, artifacts: artifacts}, nil
}

// Do runs fn in one transaction that holds the lock of the Suite. It commits
// only when fn returns nil and the context is still live; an error, a panic or
// a canceled context roll every write back. After a statement failed inside
// fn, PostgreSQL aborts the transaction: fn should return the error.
func (s *Store) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("%w: missing unit of work", governance.ErrInvalidRequest)
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin governance transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// The rollback must outlive a canceled ctx. Its own failure means the
		// connection is gone: the server aborts the transaction and the pool
		// discards the connection, so the original outcome is what matters.
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = transaction.Rollback(rollback)
	}()
	work := &tx{q: dbgen.New(transaction), project: project, suite: suite, artifacts: s.artifacts}
	if _, err := work.lock(ctx); err != nil {
		return err
	}
	if err := fn(ctx, work); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit governance transaction: %w", err)
	}
	committed = true
	return nil
}

// Constraint names of the schema that a write can violate, with the
// governance error each one means.
var (
	uniqueViolations = map[string]error{
		"suite_versions_pkey":           governance.ErrVersionConflict,
		"promotions_pkey":               governance.ErrVersionConflict,
		"promotions_reference_key":      governance.ErrVersionConflict,
		"operations_pkey":               governance.ErrOperationConflict,
		"promotions_operation_key":      governance.ErrOperationConflict,
		"consent_results_operation_key": governance.ErrOperationConflict,
		"consent_results_pkey":          governance.ErrOperationConflict,
	}
	foreignKeyViolations = map[string]error{
		"consent_results_proposal_fkey": governance.ErrNotFound,
		"operations_source_fkey":        governance.ErrInvalidState,
		"promotions_corrects_fkey":      governance.ErrNotFound,
		"promotions_revision_fkey":      governance.ErrInvalidRequest,
	}
)

const (
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
)

// translateWrite wraps a failed write, mapping the known constraint
// violations to the governance errors callers can distinguish.
func translateWrite(what string, err error) error {
	var failure *pgconn.PgError
	if errors.As(err, &failure) {
		var sentinel error
		switch failure.Code {
		case codeUniqueViolation:
			sentinel = uniqueViolations[failure.ConstraintName]
		case codeForeignKeyViolation:
			sentinel = foreignKeyViolations[failure.ConstraintName]
		}
		if sentinel != nil {
			return fmt.Errorf("%w: %s: %w", sentinel, what, err)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}
