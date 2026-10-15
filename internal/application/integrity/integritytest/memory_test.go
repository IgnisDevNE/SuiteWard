package integritytest_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
)

func TestMemoryConformance(t *testing.T) {
	integritytest.RunConformance(t, func(*testing.T) integrity.Store { return integritytest.NewMemory() })
}
