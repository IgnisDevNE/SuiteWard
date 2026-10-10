//go:build integration

package river_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

// publisher is a Publisher fake that records deliveries and fails on demand.
type publisher struct {
	mu      sync.Mutex
	calls   map[string]int
	payload map[string]json.RawMessage
	fail    error
	onCall  func(call int)       // runs inside the delivery, before it reports its outcome
	panics  func(key string) any // a non-nil result is raised as a panic
}

func (p *publisher) publish(_ context.Context, key string, payload json.RawMessage) error {
	p.mu.Lock()
	if p.calls == nil {
		p.calls, p.payload = map[string]int{}, map[string]json.RawMessage{}
	}
	p.calls[key]++
	p.payload[key] = payload
	call, fail, onCall, panics := p.calls[key], p.fail, p.onCall, p.panics
	p.mu.Unlock()
	if onCall != nil {
		onCall(call)
	}
	if panics != nil {
		if value := panics(key); value != nil {
			panic(value)
		}
	}
	return fail
}

func (p *publisher) count(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[key]
}

func newRelay(t *testing.T, w *world, c *clock, p *publisher, logs *logCapture, maxAttempts int) *river.Relay {
	t.Helper()
	relay, err := river.NewRelay(river.RelayConfig{Store: w.store, Publishers: map[string]river.Publisher{"demo": p.publish},
		MaxAttempts: maxAttempts, Lease: time.Minute, Now: c.Now, Logger: logs.logger()})
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func TestRelayDeliversADueMessageOnce(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{}
	relay := newRelay(t, w, c, p, &logCapture{}, 8)

	for range 2 {
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if p.count("k1") != 1 {
		t.Fatalf("delivered %d times, want once", p.count("k1"))
	}
	var payload struct{ N int }
	if err := json.Unmarshal(p.payload["k1"], &payload); err != nil || payload.N != 1 {
		t.Errorf("payload = %s, error %v, want the stored payload", p.payload["k1"], err)
	}
	if got := w.row("k1"); got != "delivered 1 -" {
		t.Errorf("row = %q", got)
	}
}

func TestRelayBacksOffAfterAPublisherError(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{fail: errors.New("boom")}
	relay := newRelay(t, w, c, p, &logCapture{}, 8)

	run := func() {
		t.Helper()
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if got := w.row("k1"); got != "pending 1 boom" {
		t.Fatalf("row after the failure = %q", got)
	}
	c.Advance(4 * time.Second)
	run()
	if p.count("k1") != 1 {
		t.Fatalf("retried after 4s (%d deliveries); the first backoff is longer", p.count("k1"))
	}
	c.Advance(2 * time.Second)
	p.fail = nil
	run()
	if p.count("k1") != 2 || w.row("k1") != "delivered 2 boom" {
		t.Fatalf("after the backoff: %d deliveries, row %q; want a second, successful delivery", p.count("k1"), w.row("k1"))
	}
}

func TestRelayStopsAtTheAttemptLimitWithAVisibleState(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{fail: errors.New("boom")}
	relay := newRelay(t, w, c, p, &logCapture{}, 3)

	for range 6 {
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		c.Advance(time.Hour)
	}
	if p.count("k1") != 3 || w.row("k1") != "failed 3 boom" {
		t.Fatalf("%d deliveries, row %q; want 3 attempts and a failed message with its last error", p.count("k1"), w.row("k1"))
	}
}

func TestRelayFailsAnUnknownKindAtOnce(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "mystery", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{}
	relay := newRelay(t, w, c, p, &logCapture{}, 8)

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.row("k1"); got != `failed 1 no publisher for kind "mystery"` {
		t.Fatalf("row = %q, want an immediate failure naming the kind", got)
	}
}

func TestRelayRedeliversAfterTheLeaseOfACrashedClaim(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{}
	relay := newRelay(t, w, c, p, &logCapture{}, 8)

	// A relay that claimed the message and died before recording an outcome.
	claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), postgres.ClaimOutboxParams{Now: c.Now(), Lease: time.Minute, MaxAttempts: 8, Limit: 10}))
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %v, %v", claims, err)
	}
	if err := relay.RunOnce(t.Context()); err != nil || p.count("k1") != 0 {
		t.Fatalf("a message inside its lease was delivered (%d times, error %v)", p.count("k1"), err)
	}
	c.Advance(61 * time.Second)
	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.count("k1") != 1 || w.row("k1") != "delivered 2 -" {
		t.Fatalf("%d deliveries, row %q; want one redelivery as the second attempt", p.count("k1"), w.row("k1"))
	}
}

func TestRelayIgnoresALateOutcomeOfAStaleClaim(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, logs := &clock{now: w.dbNow()}, &logCapture{}
	p := &publisher{}
	relay := newRelay(t, w, c, p, logs, 8)
	// The first delivery outlasts its lease: another relay reclaims and delivers while it still runs.
	p.onCall = func(call int) {
		if call != 1 {
			return
		}
		c.Advance(2 * time.Minute)
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Error(err)
		}
	}

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatalf("a late outcome must not be an error: %v", err)
	}
	if p.count("k1") != 2 || w.row("k1") != "delivered 2 -" {
		t.Fatalf("%d deliveries, row %q; want the newer claim's outcome to stand", p.count("k1"), w.row("k1"))
	}
	if !strings.Contains(logs.String(), "stale claim") || !strings.Contains(logs.String(), `"key":"k1"`) {
		t.Errorf("the ignored outcome was not logged: %s", logs.String())
	}
}

func TestConcurrentRelaysDeliverEachMessageOnce(t *testing.T) {
	w := newWorld(t, 5)
	for i := range 30 {
		w.enqueue(fmt.Sprintf("k%02d", i), "probe", "demo", time.Time{})
	}
	c, p := &clock{now: w.dbNow()}, &publisher{}
	var wg sync.WaitGroup
	for range 3 {
		relay := newRelay(t, w, c, p, &logCapture{}, 8)
		wg.Go(func() {
			if err := relay.RunOnce(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for i := range 30 {
		if key := fmt.Sprintf("k%02d", i); p.count(key) != 1 || w.row(key) != "delivered 1 -" {
			t.Errorf("%s: %d deliveries, row %q; want exactly one", key, p.count(key), w.row(key))
		}
	}
}

func TestRelayReportsAClaimThatFails(t *testing.T) {
	w := newWorld(t, 5)
	relay := newRelay(t, w, &clock{now: w.dbNow()}, &publisher{}, &logCapture{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := relay.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce on a canceled context = %v, want context.Canceled", err)
	}
}

// claimsOf unwraps the claims of a claim pass.
func claimsOf(result postgres.ClaimOutboxResult, err error) ([]postgres.OutboxClaim, error) {
	return result.Claims, err
}

// enqueueMany writes n system messages named k0..k(n-1), oldest first.
func enqueueMany(w *world, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
		w.enqueue(keys[i], "probe", "demo", time.Time{})
	}
	return keys
}

func TestRelayTreatsAPublisherPanicAsAPublishError(t *testing.T) {
	w := newWorld(t, 5)
	keys := enqueueMany(w, 5)
	c, logs := &clock{now: w.dbNow()}, &logCapture{}
	p := &publisher{panics: func(key string) any {
		if key == "k2" {
			return "kaboom " + strings.Repeat("x", 5000)
		}
		return nil
	}}
	relay := newRelay(t, w, c, p, logs, 8)

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatalf("a publisher panic must not abort the run: %v", err)
	}
	for _, key := range keys {
		if key == "k2" {
			continue
		}
		if got := w.row(key); got != "delivered 1 -" {
			t.Errorf("%s = %q, want the rest of the batch delivered in the same run", key, got)
		}
	}
	got := w.row("k2")
	if !strings.HasPrefix(got, "pending 1 publisher panicked: kaboom") || len(got) > 1100 {
		t.Errorf("k2 = %.80q (%d bytes), want a retry after a truncated panic text", got, len(got))
	}
	if strings.Contains(logs.String(), strings.Repeat("x", 2000)) {
		t.Error("the log carries the whole panic value")
	}
}

func TestRelayFailsAMessageWhoseLastAttemptPanics(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, p := &clock{now: w.dbNow()}, &publisher{panics: func(string) any { return errors.New("boom") }}
	relay := newRelay(t, w, c, p, &logCapture{}, 1)

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.row("k1"); got != "failed 1 publisher panicked: boom" {
		t.Fatalf("row = %q, want the last attempt to end in the failed state", got)
	}
}

func TestRelayTruncatesLongErrorTexts(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	c, logs := &clock{now: w.dbNow()}, &logCapture{}
	p := &publisher{fail: errors.New(strings.Repeat("é", 1500))} // multi-byte: a cut must not split a character
	relay := newRelay(t, w, c, p, logs, 8)

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := w.pool.QueryRow(t.Context(), "SELECT last_error FROM outbox WHERE key='k1'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) > 1024+len("…") || !strings.HasSuffix(stored, "…") || !strings.HasPrefix(stored, "é") {
		t.Errorf("stored error is %d bytes ending %q, want at most 1 KiB and an ellipsis", len(stored), stored[max(0, len(stored)-8):])
	}
	if strings.Contains(logs.String(), strings.Repeat("é", 600)) {
		t.Error("the log carries more than the truncated error")
	}
}

func TestRelayGivesBackUntriedClaimsWhenTheRunIsCanceled(t *testing.T) {
	w := newWorld(t, 5)
	enqueueMany(w, 5)
	c, p := &clock{now: w.dbNow()}, &publisher{}
	relay := newRelay(t, w, c, p, &logCapture{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	p.onCall = func(int) { cancel() } // the process is told to stop during the first delivery

	if err := relay.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce reported success although its context was canceled")
	}
	if got := w.scalar("SELECT count(*) FILTER (WHERE attempts = 0) || ' ' || count(*) FILTER (WHERE attempts = 1) FROM outbox"); got != "4 1" {
		t.Fatalf("untried and tried messages = %q, want the four untried ones given back with their attempt", got)
	}

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.scalar("SELECT count(*) FILTER (WHERE state = 'delivered' AND attempts = 1) || ' ' || count(*) FILTER (WHERE state = 'pending' AND attempts = 1) FROM outbox"); got != "4 1" {
		t.Fatalf("after the next run (delivered, still leased) = %q, want the four released messages delivered at once as their first attempt", got)
	}
}

// failingFinish fails the outcome write of one delivery.
type failingFinish struct {
	*postgres.Store
	mu     sync.Mutex
	calls  int
	failAt int
}

func (s *failingFinish) FinishOutbox(ctx context.Context, claim postgres.OutboxClaim, update postgres.OutboxUpdate) (bool, error) {
	s.mu.Lock()
	s.calls++
	fail := s.calls == s.failAt
	s.mu.Unlock()
	if fail {
		return false, errors.New("outcome write failed")
	}
	return s.Store.FinishOutbox(ctx, claim, update)
}

func TestRelayGivesBackUntriedClaimsWhenAnOutcomeCannotBeWritten(t *testing.T) {
	w := newWorld(t, 5)
	enqueueMany(w, 5)
	c, p := &clock{now: w.dbNow()}, &publisher{}
	relay, err := river.NewRelay(river.RelayConfig{Store: &failingFinish{Store: w.store, failAt: 2}, Publishers: map[string]river.Publisher{"demo": p.publish},
		MaxAttempts: 8, Lease: time.Minute, Now: c.Now, Logger: (&logCapture{}).logger()})
	if err != nil {
		t.Fatal(err)
	}

	if err := relay.RunOnce(t.Context()); err == nil {
		t.Fatal("RunOnce reported success although an outcome could not be written")
	}
	if got := w.scalar("SELECT count(*) FILTER (WHERE state = 'delivered') || ' ' || count(*) FILTER (WHERE attempts = 0) || ' ' || count(*) FILTER (WHERE state = 'pending' AND attempts = 1) FROM outbox"); got != "1 3 1" {
		t.Fatalf("delivered, untried and tried-without-outcome = %q, want 1 3 1", got)
	}
}

func TestRelayLogsAMessageItAbandoned(t *testing.T) {
	w := newWorld(t, 5)
	w.enqueue("k1", "probe", "demo", time.Time{})
	if _, err := w.pool.Exec(t.Context(), `UPDATE outbox SET attempts = 3`); err != nil {
		t.Fatal(err)
	}
	c, logs, p := &clock{now: w.dbNow()}, &logCapture{}, &publisher{}
	relay := newRelay(t, w, c, p, logs, 3)

	if err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.row("k1"); !strings.HasPrefix(got, "failed 3 delivery abandoned") || p.count("k1") != 0 {
		t.Fatalf("row %q after %d deliveries, want the poison message failed without a delivery", got, p.count("k1"))
	}
	for _, want := range []string{"abandoned", `"key":"k1"`, `"kind":"demo"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), `"n":1`) {
		t.Error("the log carries a payload")
	}
}
