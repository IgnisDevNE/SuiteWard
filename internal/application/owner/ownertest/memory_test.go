package ownertest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestMemoryConformance(t *testing.T) {
	ownertest.RunConformance(t, func(*testing.T) (owner.Store, ownertest.SeedSuite) {
		memory := ownertest.NewMemory()
		return memory, func(project contract.ProjectID, _ contract.PolicyRevisionID) error {
			memory.AddSuite(project)
			return nil
		}
	})
}
