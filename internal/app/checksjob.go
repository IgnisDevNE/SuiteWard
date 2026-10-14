package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

type checkReader interface {
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

type checksJob struct {
	repos    []string
	reader   checkReader
	owners   owner.Store
	suites   governance.UnitOfWork
	contents governance.ContentReader
	content  governance.ContentWriter
	ledger   checks.Store
	commands commands.Store
	pulls    pullLister
	tier     string
	problems *bootstrapProblems
	logger   *slog.Logger
	now      func() time.Time
}

func (j *checksJob) run(ctx context.Context) error { return nil }
