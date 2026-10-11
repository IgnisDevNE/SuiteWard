package app

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

var errNotImplemented = errors.New("app: not implemented")

// repositoryStatus is one entry of the github section of /status.
type repositoryStatus struct {
	Repo        string `json:"repo"`
	LastSuccess string `json:"lastSuccess,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	WaitUntil   string `json:"waitUntil,omitempty"`
}

// repositoryReader is the part of the observation store /status reads.
type repositoryReader interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
}

// githubStatus reports what synchronization last saw of the configured repositories.
type githubStatus struct {
	repos []string
	store repositoryReader
}

func (g *githubStatus) report(ctx context.Context) ([]repositoryStatus, error) {
	return nil, errNotImplemented
}

// githubSync is the GitHub side of the service, composed only when the GitHub settings are set.
type githubSync struct {
	reconciler *githubsync.Reconciler
	repos      []string
	jobTimeout time.Duration
}

func composeGitHub(g config.GitHub, jobTimeout time.Duration, store githubsync.Store) (*githubSync, error) {
	return nil, errNotImplemented
}

func (s *githubSync) tick(ctx context.Context) error { return errNotImplemented }

func (s *githubSync) syncRepo(ctx context.Context, repo string) error { return errNotImplemented }

// classifyReadError turns an error of the GitHub adapter into a githubsync.ReadError.
func classifyReadError(err error) error { return errNotImplemented }

// tickDeadline is how long a tick may start repositories: JOB_TIMEOUT minus 5 s.
func tickDeadline(jobTimeout time.Duration) time.Duration { return 0 }
