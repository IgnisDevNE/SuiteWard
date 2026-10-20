package river

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// finishRecorder is a scriptedStore that keeps what the relay writes as the outcome.
type finishRecorder struct {
	scriptedStore
	updates []postgres.OutboxUpdate
}

func (s *finishRecorder) FinishOutbox(ctx context.Context, claim postgres.OutboxClaim, update postgres.OutboxUpdate) (bool, error) {
	s.updates = append(s.updates, update)
	return s.scriptedStore.FinishOutbox(ctx, claim, update)
}

// panicOutcome runs one claim whose publisher panics with value and returns the stored LastError and the log.
func panicOutcome(t *testing.T, value any) (lastError, logs string) {
	t.Helper()
	store := &finishRecorder{scriptedStore: scriptedStore{claims: []postgres.OutboxClaim{{Key: "k", Kind: "demo", Attempts: 1}}}}
	var log bytes.Buffer
	relay, err := NewRelay(RelayConfig{Store: store, Publishers: map[string]Publisher{"demo": func(context.Context, string, json.RawMessage) error { panic(value) }},
		MaxAttempts: 3, Lease: time.Minute, Logger: slog.New(slog.NewTextHandler(&log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.updates) != 1 {
		t.Fatalf("%d outcomes written, want one", len(store.updates))
	}
	return store.updates[0].LastError, log.String()
}

// A publisher panic can carry anything the publisher held, a payload or a secret-shaped value included. The relay
// stores and logs a classification and a short digest, which tells two panics apart without repeating the text.
// Unit level: the fake store needs no PostgreSQL.
func TestRelayNeverStoresOrLogsAPanicText(t *testing.T) {
	const secret = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	const marker = "<!-- suiteward:status v1 -->"
	first, firstLog := panicOutcome(t, marker+" token="+secret+strings.Repeat("x", 2000))

	if !regexp.MustCompile(`^publisher panicked \(digest [0-9a-f]{8}\)$`).MatchString(first) {
		t.Errorf("LastError = %.80q, want the classification and a digest only", first)
	}
	for _, text := range []string{secret, marker, "xxxx"} {
		if strings.Contains(first, text) || strings.Contains(firstLog, text) {
			t.Errorf("the stored error or the log carries %q", text)
		}
	}
	if !strings.Contains(firstLog, first) {
		t.Errorf("the log does not carry the stored classification %q:\n%s", first, firstLog)
	}
	if again, _ := panicOutcome(t, marker+" token="+secret+strings.Repeat("x", 2000)); again != first {
		t.Errorf("the same panic gave %q then %q; the digest must be stable", first, again)
	}
	if other, _ := panicOutcome(t, "another panic"); other == first {
		t.Errorf("two different panics gave the same digest %q", first)
	}
}
