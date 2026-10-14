package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// Outbox identity of the bootstrap comments (docs/contracts/runtime.md, "Outbox kind github_comment").
const (
	githubCommentKind     = "github_comment"
	bootstrapSummaryKey   = "bootstrap-summary"
	bootstrapPromotedKey  = "bootstrap-promoted"
	maxBootstrapTextBytes = 150
)

// commentTarget is what every github_comment payload carries: the repository, and the PR to post on.
type commentTarget struct {
	RepositoryID int64  `json:"repositoryId"`
	Repo         string `json:"repo"` // owner/name
	Pull         int    `json:"pull"`
}

// bootstrapSummaryPayload is the payload of bootstrap-summary:<repository id>:P<n>-R1.
type bootstrapSummaryPayload struct {
	commentTarget
	Summary bootstrapSummaryInputs `json:"summary"`
}

// bootstrapSummaryInputs are the inputs of commandtext.Summary.
type bootstrapSummaryInputs struct {
	Proposal  int      `json:"proposal"`
	Revision  int      `json:"revision"`
	Origin    string   `json:"origin"`
	Branch    string   `json:"branch"`
	Protected int      `json:"protected"`
	Paths     []string `json:"paths"`
	Include   []string `json:"include"`
	Exclude   []string `json:"exclude"`
	Tier      string   `json:"tier"`
}

// bootstrapPromotedPayload is the payload of bootstrap-promoted:<repository id>:P<n>-R1.
type bootstrapPromotedPayload struct {
	commentTarget
	Reference string `json:"reference"`
	Version   string `json:"version"`
}

// bootstrapReader is the part of the GitHub reader the bootstrap uses.
type bootstrapReader interface {
	Repository(ctx context.Context, repo string) (github.Repository, error)
	BranchHead(ctx context.Context, repo, branch string) (string, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// pullLister is the part of the observation store the bootstrap reads.
type pullLister interface {
	Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error)
}

// bootstrapJob is the github_bootstrap job.
type bootstrapJob struct {
	repos   []string
	reader  bootstrapReader
	owners  owner.Store
	suites  governance.UnitOfWork
	content governance.ContentWriter
	pulls   pullLister
	tier    string
	logger  *slog.Logger
	now     func() time.Time
}

func (b *bootstrapJob) run(ctx context.Context) error { return nil }

func cutBytes(s string, max int) string { return s }

func (s bootstrapSummaryInputs) summary() (commandtext.Summary, error) {
	return commandtext.Summary{}, nil
}

func newSummaryMessage(target commentTarget, inputs bootstrapSummaryInputs) (governance.OutboxMessage, error) {
	return governance.OutboxMessage{}, nil
}
