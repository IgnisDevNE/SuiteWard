package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// contentVerifier is the part of the artifact store the integrity job uses.
type contentVerifier interface {
	Verify(ctx context.Context, digest artifact.Digest) error
}

// integrityJob is the canonical_integrity job.
type integrityJob struct {
	owners  owner.Store
	suites  governance.UnitOfWork
	content contentVerifier
	store   integrity.Store
	logger  *slog.Logger
	now     func() time.Time
}

func (j *integrityJob) run(ctx context.Context) error { return nil }
