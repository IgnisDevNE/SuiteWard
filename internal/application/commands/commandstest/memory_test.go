package commandstest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

func TestMemoryConformance(t *testing.T) {
	commandstest.RunConformance(t, func(*testing.T) commandstest.Env {
		owners, observations := ownertest.NewMemory(), githubsynctest.NewMemory()
		memory := commandstest.NewMemory(owners, observations)
		return commandstest.Env{Store: memory, Owner: owners, Sync: observations, OutboxKeys: memory.OutboxKeys}
	})
}
