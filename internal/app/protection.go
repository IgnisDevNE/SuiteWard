package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
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

// protectionCheck is the github_protection_check job.
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
	return errors.New("protection check: not implemented")
}

// storedProtection returns the latest stored result of each configured
// repository the check has identified, by configured name.
func storedProtection(ctx context.Context, owners ownerReader, ids *repositoryIDs, repos []string) (map[string]owner.ProtectionResult, error) {
	return map[string]owner.ProtectionResult{}, nil
}

// protectionStatus is the protection entry of a repository in /status.
type protectionStatus struct {
	Result    string   `json:"result"`
	Findings  []string `json:"findings"`
	Caveats   []string `json:"caveats"`
	CheckedAt string   `json:"checkedAt"`
}
