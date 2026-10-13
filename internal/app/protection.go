package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// repositoryIDs maps the configured repository names to the numeric ids the
// protection check read from GitHub: /status and MCP status show a stored
// result, which is keyed by id, under the configured name.
type repositoryIDs struct {
	mu     sync.Mutex
	byName map[string]int64
}

func (r *repositoryIDs) set(name string, id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = map[string]int64{}
	}
	r.byName[name] = id
}

func (r *repositoryIDs) lookup(name string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, found := r.byName[name]
	return id, found
}

// protectionCheck is the github_protection_check job: it verifies every
// configured repository and records the result. Only a store error fails it;
// whatever GitHub answers is a result, or a wait until the next run.
type protectionCheck struct {
	repos   []string
	appID   int64
	readers setupReaders
	store   owner.Store
	ids     *repositoryIDs
	logger  *slog.Logger
	now     func() time.Time
}

func (c *protectionCheck) run(ctx context.Context) error {
	enrollment, _, err := c.store.Current(ctx) // before setup the state is empty: no project, so no test check
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	for _, name := range c.repos {
		if err := ctx.Err(); err != nil { // a cancelled job must not record what its cancelled reads classify as failures
			return err
		}
		if err := c.verify(ctx, name, enrollment.Projects); err != nil {
			return err
		}
	}
	return nil
}

// verify reads the repository with the synchronization token, which gives its
// id and default branch, and then its protection with the protection token.
func (c *protectionCheck) verify(ctx context.Context, name string, projects []owner.ProjectPolicy) error {
	repo, err := c.readers.read.Repository(ctx, name)
	if err != nil {
		return c.repositoryUnreadable(ctx, name, err)
	}
	c.ids.set(name, repo.ID)
	testCheck := "" // the repository's configured test check; none before setup
	for _, project := range projects {
		if project.RepositoryID == repo.ID {
			testCheck = project.TestCheck
		}
	}
	result, waiting := evaluateProtection(ctx, c.logger, c.readers.protect, c.appID, name, repo, testCheck)
	if err := ctx.Err(); err != nil {
		return err
	}
	if waiting {
		c.logger.InfoContext(ctx, "github protection check waits for the next run", "repo", name)
		return nil
	}
	return c.record(ctx, name, repo.ID, result)
}

// repositoryUnreadable handles a failed repository read: a wait skips the
// repository, and any other failure is recorded as unknown when an earlier read
// identified the repository. Without an id there is nothing to record against.
func (c *protectionCheck) repositoryUnreadable(ctx context.Context, name string, err error) error {
	logReadFailure(ctx, c.logger, "github repository read failed", name, err)
	if err := ctx.Err(); err != nil {
		return err
	}
	if waits(err) {
		return nil
	}
	id, known := c.ids.lookup(name)
	if !known {
		c.logger.WarnContext(ctx, "github protection check skipped: the repository has not been identified", "repo", name)
		return nil
	}
	return c.record(ctx, name, id, protection.Unknown())
}

func (c *protectionCheck) record(ctx context.Context, name string, id int64, result protection.Result) error {
	if err := c.store.RecordProtection(ctx, protectionRecord(id, result, c.now())); err != nil {
		return fmt.Errorf("record the protection result of %s: %w", name, err)
	}
	c.logger.InfoContext(ctx, "github protection verified", "repo", name, "result", string(result.Kind))
	return nil
}

// waits reports whether a read ended on a rate limit or an exhausted budget.
func waits(err error) bool {
	kind := readFailure(err).Kind
	return kind == githubsync.FailureRateLimited || kind == githubsync.FailureBudgetExhausted
}

// storedProtection returns the latest stored result of each configured
// repository the check has identified, by configured name.
func storedProtection(ctx context.Context, owners ownerReader, ids *repositoryIDs, repos []string) (map[string]owner.ProtectionResult, error) {
	stored, err := owners.Protection(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the protection results: %w", err)
	}
	byID := make(map[int64]owner.ProtectionResult, len(stored))
	for _, result := range stored {
		byID[result.RepositoryID] = result
	}
	found := make(map[string]owner.ProtectionResult, len(repos))
	for _, name := range repos {
		if id, known := ids.lookup(name); known {
			if result, listed := byID[id]; listed {
				found[name] = result
			}
		}
	}
	return found, nil
}

// protectionStatus is the protection entry of a repository in /status.
type protectionStatus struct {
	Result    string   `json:"result"`
	Findings  []string `json:"findings"`
	Caveats   []string `json:"caveats"`
	CheckedAt string   `json:"checkedAt"`
}
