package river

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 6: 160 * time.Second, 7: 5 * time.Minute, 8: 5 * time.Minute, 1000: 5 * time.Minute} {
		if got := backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}

// scriptedStore is an OutboxStore whose calls fail on demand.
type scriptedStore struct {
	claims      []postgres.OutboxClaim
	claimErr    error
	finishErr   error
	finishCalls int
	released    []string
	releaseErr  error
	releaseLive bool // the context of the release was still usable
}

func (s *scriptedStore) ClaimOutbox(context.Context, postgres.ClaimOutboxParams) (postgres.ClaimOutboxResult, error) {
	claims := s.claims
	s.claims = nil
	return postgres.ClaimOutboxResult{Claims: claims}, s.claimErr
}

func (s *scriptedStore) ReleaseOutbox(ctx context.Context, claim postgres.OutboxClaim, _ time.Time) (bool, error) {
	s.released = append(s.released, claim.Key)
	s.releaseLive = ctx.Err() == nil
	return true, s.releaseErr
}

func (s *scriptedStore) FinishOutbox(context.Context, postgres.OutboxClaim, postgres.OutboxUpdate) (bool, error) {
	s.finishCalls++
	return true, s.finishErr
}

func relayFor(t *testing.T, store OutboxStore) *Relay {
	t.Helper()
	relay, err := NewRelay(RelayConfig{Store: store, Publishers: map[string]Publisher{"demo": func(context.Context, string, json.RawMessage) error { return nil }},
		MaxAttempts: 3, Lease: time.Minute, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func TestRelayReturnsStoreErrors(t *testing.T) {
	boom := errors.New("store down")
	if err := relayFor(t, &scriptedStore{claimErr: boom}).RunOnce(t.Context()); !errors.Is(err, boom) {
		t.Errorf("claim failure = %v, want the store error", err)
	}
	store := &scriptedStore{claims: []postgres.OutboxClaim{{Key: "k", Kind: "demo", Attempts: 1}}, finishErr: boom}
	if err := relayFor(t, store).RunOnce(t.Context()); !errors.Is(err, boom) || store.finishCalls != 1 {
		t.Errorf("outcome failure = %v after %d outcome writes, want the store error", err, store.finishCalls)
	}
}

func TestNewRelayChecksItsDependencies(t *testing.T) {
	valid := RelayConfig{Store: &scriptedStore{}, MaxAttempts: 1, Lease: time.Second, Logger: slog.New(slog.DiscardHandler)}
	for name, mutate := range map[string]func(*RelayConfig){
		"no store":      func(c *RelayConfig) { c.Store = nil },
		"no logger":     func(c *RelayConfig) { c.Logger = nil },
		"no attempts":   func(c *RelayConfig) { c.MaxAttempts = 0 },
		"no lease":      func(c *RelayConfig) { c.Lease = 0 },
		"nil publisher": func(c *RelayConfig) { c.Publishers = map[string]Publisher{"demo": nil} },
	} {
		config := valid
		mutate(&config)
		if _, err := NewRelay(config); err == nil {
			t.Errorf("%s: NewRelay accepted the configuration", name)
		}
	}
	if _, err := NewRelay(valid); err != nil {
		t.Errorf("valid configuration rejected: %v", err)
	}
}

func TestProbeOnlyLogs(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	if err := ProbeHandler(logger)(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := ProbePublisher(logger)(t.Context(), "probe-1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "probe job") || !strings.Contains(out.String(), `"key":"probe-1"`) {
		t.Fatalf("probe log = %s, want the job and the published key", out.String())
	}
}

func TestRelayReleasesTheClaimsItNeverTriedOnAnAbortedRun(t *testing.T) {
	boom := errors.New("outcome write failed")
	store := &scriptedStore{claims: []postgres.OutboxClaim{{Key: "a", Kind: "demo", Attempts: 1}, {Key: "b", Kind: "demo", Attempts: 1}, {Key: "c", Kind: "demo", Attempts: 1}}, finishErr: boom}

	if err := relayFor(t, store).RunOnce(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("RunOnce = %v, want the outcome error", err)
	}
	if !slices.Equal(store.released, []string{"b", "c"}) {
		t.Fatalf("released %v, want exactly the untried claims b and c", store.released)
	}
}

func TestRelayReleasesOnAContextTheAbortDidNotCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &scriptedStore{claims: []postgres.OutboxClaim{{Key: "a", Kind: "demo", Attempts: 1}, {Key: "b", Kind: "demo", Attempts: 1}}, finishErr: context.Canceled}
	relay, err := NewRelay(RelayConfig{Store: store, Publishers: map[string]Publisher{"demo": func(context.Context, string, json.RawMessage) error {
		cancel()
		return ctx.Err()
	}}, MaxAttempts: 3, Lease: time.Minute, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	if err := relay.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce = %v, want the cancellation", err)
	}
	if !slices.Equal(store.released, []string{"b"}) || !store.releaseLive {
		t.Fatalf("released %v on a live context = %v, want b released although the run was canceled", store.released, store.releaseLive)
	}
}

func TestAFailedReleaseDoesNotHideTheAbortError(t *testing.T) {
	boom := errors.New("outcome write failed")
	store := &scriptedStore{claims: []postgres.OutboxClaim{{Key: "a", Kind: "demo", Attempts: 1}, {Key: "b", Kind: "demo", Attempts: 1}}, finishErr: boom, releaseErr: errors.New("release failed")}

	if err := relayFor(t, store).RunOnce(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("RunOnce = %v, want the original outcome error", err)
	}
}

func TestTruncateNeverSplitsACharacter(t *testing.T) {
	text := strings.Repeat("€", 500) // three bytes each: 1024 falls inside a character
	got := truncate(text)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > maxErrorBytes+len("…") {
		t.Fatalf("truncate = %d bytes, valid UTF-8 %v, want a short valid text ending in an ellipsis", len(got), utf8.ValidString(got))
	}
	if short := "short"; truncate(short) != short {
		t.Error("a short text was changed")
	}
}
