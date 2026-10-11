package githubsynctest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
)

func TestMemoryConformance(t *testing.T) {
	githubsynctest.RunConformance(t, func(*testing.T) githubsync.Store { return githubsynctest.NewMemory() })
}
