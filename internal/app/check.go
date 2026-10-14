package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// The github_check publisher (docs/contracts/runtime.md, "Outbox kind github_check").

const (
	githubCheckKind = "github_check"
	checkName       = "SuiteWard / Contract"
)

type checkPayload struct{}

func newCheckMessage(repo string, r checks.Result, tier, version string, generation int64) (governance.OutboxMessage, error) {
	return governance.OutboxMessage{}, errors.New("not implemented")
}

type checkRunner interface {
	Post(ctx context.Context, repo string, run github.CheckRun) (int64, error)
	Runs(ctx context.Context, repo, sha, name string, appID int64) ([]github.CheckRunSummary, error)
}

type checkLedger interface {
	Current(ctx context.Context, repositoryID int64, pull int) (checks.Record, bool, error)
}

type checkPublisher struct {
	runs     checkRunner
	ledger   checkLedger
	instance *lazy[string]
	appID    int64
	logger   *slog.Logger
}

func (p *checkPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	return errors.New("not implemented")
}
