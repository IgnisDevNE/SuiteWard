package github

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// CheckRun is a check run to create.
type CheckRun struct {
	Name, HeadSHA, Status, Conclusion, ExternalID, Title, Summary string
}

// CheckRunSummary is one listed check run.
type CheckRunSummary struct {
	ID                             int64
	Status, Conclusion, ExternalID string
}

// Checks creates and lists check runs.
type Checks struct {
	client *rest.Client
}

// NewChecks returns a check writer over client.
func NewChecks(client *rest.Client) (*Checks, error) {
	if client == nil {
		return nil, errors.New("github checks: nil client")
	}
	return &Checks{client: client}, nil
}

// Post creates run on repo and returns its id.
func (c *Checks) Post(ctx context.Context, repo string, run CheckRun) (int64, error) {
	return 0, errors.New("not implemented")
}

// Runs lists the latest check runs of sha named name by the App appID.
func (c *Checks) Runs(ctx context.Context, repo, sha, name string, appID int64) ([]CheckRunSummary, error) {
	return nil, errors.New("not implemented")
}
