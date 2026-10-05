package governancetest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
)

func TestMemoryConformance(t *testing.T) {
	governancetest.RunConformance(t, func(*testing.T) governancetest.Store { return governancetest.NewMemory() })
}
