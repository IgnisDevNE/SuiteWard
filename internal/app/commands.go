package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// commandsJob is the github_commands job (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
type commandsJob struct {
	ledger     commands.Store
	owners     owner.Store
	suites     governance.UnitOfWork
	syncs      repositoryReader // the observation store: each repository's last successful cycle
	bot        *botAccount
	jobTimeout time.Duration
	logger     *slog.Logger
	now        func() time.Time

	failing commandFailure // the candidate that failed last, in this process
}

type commandFailure struct {
	repository, comment int64
	attempts            int
}

func (j *commandsJob) run(ctx context.Context) error { return nil }
