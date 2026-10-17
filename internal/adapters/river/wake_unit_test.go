package river

import (
	"testing"
	"time"
)

func TestTheDefaultSyncIntervalIsTwentySeconds(t *testing.T) {
	if defaultSyncInterval != 20*time.Second {
		t.Fatalf("defaultSyncInterval = %v; want 20s (docs/contracts/runtime.md, Sync pacing)", defaultSyncInterval)
	}
}
