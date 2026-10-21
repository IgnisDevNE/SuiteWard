// Package postgres implements governance persistence on PostgreSQL: one
// transaction per unit of work, with the Suite row locked for its duration.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// ErrSchemaNotReady reports a database whose schema is not usable by this build: not the supported version, not
// there, or not visible to the role. A database that cannot be reached, or that refuses the login, is not this.
var ErrSchemaNotReady = errors.New("postgres schema is not ready")

// ErrSchemaNewer is ErrSchemaNotReady for a schema migrated by a newer release than this build.
var ErrSchemaNewer = errors.New("postgres schema is newer than this build")

// schemaProblems are the SQLSTATEs that say the schema is not there or not usable by the role: an undefined function
// (the version function of a database nobody migrated), an undefined table, insufficient privilege and an invalid
// schema name.
var schemaProblems = []string{"42883", "42P01", "42501", "3F000"}

const (
	schemaCheckTimeout = 10 * time.Second
	rollbackTimeout    = 10 * time.Second
)

// ArtifactVerifier confirms that stored content is available and matches its
// digest. *filesystem.Store satisfies it.
type ArtifactVerifier interface {
	Verify(ctx context.Context, digest artifact.Digest) error
}

// JobInserter inserts a job through the transaction of EnqueueSystem and returns its id. The River adapter implements it.
type JobInserter interface {
	Insert(ctx context.Context, tx pgx.Tx, job governance.Job) (int64, error)
}

// Store implements governance.UnitOfWork on PostgreSQL. Seed, the test-only governance.Seeder, is in seed_test.go.
type Store struct {
	pool      *pgxpool.Pool
	artifacts ArtifactVerifier
	jobs      JobInserter
}

var _ governance.UnitOfWork = (*Store)(nil)

// NewStore returns a Store after checking that the database is at the schema
// version this build supports. The artifact verifier is consulted only when a
// version is written; the job inserter is used for every enqueued job.
func NewStore(pool *pgxpool.Pool, artifacts ArtifactVerifier, jobs JobInserter) (*Store, error) {
	if pool == nil || artifacts == nil || jobs == nil {
		return nil, errors.New("postgres store requires a connection pool, an artifact verifier and a job inserter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), schemaCheckTimeout)
	defer cancel()
	version, err := dbgen.New(pool).GetSchemaVersion(ctx)
	if err != nil {
		var answered *pgconn.PgError
		if errors.As(err, &answered) && slices.Contains(schemaProblems, answered.Code) {
			return nil, fmt.Errorf("%w: read schema version: %w", ErrSchemaNotReady, err)
		}
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if err := supportedSchema(version); err != nil {
		return nil, err
	}
	return &Store{pool: pool, artifacts: artifacts, jobs: jobs}, nil
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
	return inTx(ctx, s.pool, "governance", func(transaction pgx.Tx) error {
		work := &tx{q: dbgen.New(transaction), project: project, suite: suite, artifacts: s.artifacts}
		if _, err := work.lock(ctx); err != nil {
			return err
		}
		return fn(ctx, work)
	})
}

// inTx runs fn in one transaction of pool. It commits only when fn returns nil and ctx is still live; an error, a panic
// or a canceled context roll every write back. kind names the transaction in the errors of the begin and the commit.
func inTx(ctx context.Context, pool *pgxpool.Pool, kind string, fn func(pgx.Tx) error) error {
	transaction, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s transaction: %w", kind, err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	if err := fn(transaction); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return translateWrite("commit "+kind+" transaction", err)
	}
	committed = true
	return nil
}

// rollback abandons a transaction that did not commit. It must outlive a
// canceled ctx. Its own failure means the connection is gone: the server
// aborts the transaction and the pool discards the connection, so the
// original outcome is what matters.
func rollback(ctx context.Context, transaction pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	_ = transaction.Rollback(rollbackCtx)
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
		"consent_results_scope_key":     governance.ErrOperationConflict,
		"outbox_pkey":                   governance.ErrOperationConflict,
		"proposal_revisions_pkey":       governance.ErrProposalConflict,
		"pr_merges_pkey":                governance.ErrConflict,
	}
	foreignKeyViolations = map[string]error{
		"consent_results_proposal_fkey": governance.ErrNotFound,
		"operations_source_fkey":        governance.ErrInvalidState,
		"promotions_corrects_fkey":      governance.ErrNotFound,
		"promotions_revision_fkey":      governance.ErrInvalidRequest,
		"pr_merges_version_fkey":        governance.ErrNotFound,
		"pr_merges_corrects_fkey":       governance.ErrNotFound,
		"pr_merges_revision_fkey":       governance.ErrInvalidRequest,
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

// CreateSuite creates an empty Suite under an existing policy revision. It
// needs no transaction: policies are immutable, and the insert either creates
// the row or finds the one a concurrent caller committed, which is then read
// and compared.
func (s *Store) CreateSuite(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(suite)) == "" || strings.TrimSpace(string(policy)) == "" || strings.TrimSpace(string(target)) == "" {
		return fmt.Errorf("%w: a suite needs a project, an id, a policy and a target", governance.ErrInvalidRequest)
	}
	q := dbgen.New(s.pool)
	if _, err := q.GetPolicy(ctx, dbgen.GetPolicyParams{ProjectID: string(project), RevisionID: string(policy)}); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: policy %q of project %q", governance.ErrNotFound, policy, project)
	} else if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}
	created, err := q.InsertNewSuite(ctx, dbgen.InsertNewSuiteParams{ProjectID: string(project), SuiteID: string(suite), TargetID: string(target), PolicyRevisionID: string(policy)})
	if err != nil {
		return translateWrite("insert suite", err)
	}
	if created == 1 {
		return nil
	}
	existing, err := q.GetSuite(ctx, dbgen.GetSuiteParams{ProjectID: string(project), SuiteID: string(suite)})
	if err != nil {
		return fmt.Errorf("load existing suite: %w", err)
	}
	if existing.PolicyRevisionID != string(policy) || existing.TargetID != string(target) {
		return fmt.Errorf("%w: suite %q of project %q is governed by policy %q targeting %q", governance.ErrConflict, suite, project, existing.PolicyRevisionID, existing.TargetID)
	}
	return nil
}
