package postgres_test

import (
	"math"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// ClaimOutbox refuses a bound the database cannot hold before it touches the pool, so a Store without one is enough.
func TestClaimOutboxRefusesBoundsOutsideOneToInt32(t *testing.T) {
	for name, params := range map[string]postgres.ClaimOutboxParams{
		"zero attempts":       {Lease: time.Minute, MaxAttempts: 0, Limit: 1},
		"attempts past int32": {Lease: time.Minute, MaxAttempts: math.MaxInt32 + 1, Limit: 1},
		"zero limit":          {Lease: time.Minute, MaxAttempts: 1, Limit: 0},
		"limit past int32":    {Lease: time.Minute, MaxAttempts: 1, Limit: math.MaxInt32 + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (&postgres.Store{}).ClaimOutbox(t.Context(), params); err == nil {
				t.Error("ClaimOutbox accepted the bound")
			}
		})
	}
}

// FinishOutbox and ReleaseOutbox refuse an attempt count the database cannot hold before they touch the pool.
func TestFinishAndReleaseOutboxRefuseAttemptsPastInt32(t *testing.T) {
	claim := postgres.OutboxClaim{Key: "message", Attempts: math.MaxInt32 + 1}
	if _, err := (&postgres.Store{}).FinishOutbox(t.Context(), claim, postgres.OutboxUpdate{}); err == nil {
		t.Error("FinishOutbox accepted the attempt count")
	}
	if _, err := (&postgres.Store{}).ReleaseOutbox(t.Context(), claim, time.Now()); err == nil {
		t.Error("ReleaseOutbox accepted the attempt count")
	}
}
