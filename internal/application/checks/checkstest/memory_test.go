package checkstest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks/checkstest"
)

func TestMemoryConformance(t *testing.T) {
	checkstest.RunConformance(t, func(*testing.T) checkstest.Env {
		memory := checkstest.NewMemory()
		return checkstest.Env{Store: memory, OutboxKeys: memory.OutboxKeys, FailMessage: memory.FailMessage}
	})
}
