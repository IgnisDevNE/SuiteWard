package postgres_test

import (
	"math"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// Pending refuses a limit the database cannot hold, instead of capping it, before it touches the pool.
func TestPendingRefusesALimitPastInt32(t *testing.T) {
	if _, err := (&postgres.Store{}).Pending(t.Context(), time.Now(), math.MaxInt32+1); err == nil {
		t.Error("Pending accepted a limit past int32")
	}
}
