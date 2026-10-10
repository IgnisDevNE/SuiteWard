//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

var queueObject = json.RawMessage(`{"suite":"suite"}`)

func (w *pgWorld) scalar(query string, args ...any) (value string) {
	w.t.Helper()
	if err := w.pool.QueryRow(w.t.Context(), query, args...).Scan(&value); err != nil {
		w.t.Fatal(err)
	}
	return value
}

func TestEnqueueStoresAJobThatCarriesItsRequest(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	when := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)

	err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		if err := tx.Enqueue(ctx, governance.Job{Kind: "probe", Args: queueObject}); err != nil {
			return err
		}
		return tx.Enqueue(ctx, governance.Job{Kind: "later", Args: queueObject, ScheduledAt: when})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.scalar("SELECT state::text || ' ' || max_attempts || ' ' || args::text FROM river_job WHERE kind='probe'"); got != `available 5 {"suite": "suite"}` {
		t.Errorf("immediate job = %q", got)
	}
	if got := w.scalar("SELECT state::text FROM river_job WHERE kind='later'"); got != "scheduled" {
		t.Errorf("future job state = %q, want scheduled", got)
	}
	var scheduled time.Time
	if err := w.pool.QueryRow(t.Context(), "SELECT scheduled_at FROM river_job WHERE kind='later'").Scan(&scheduled); err != nil || !scheduled.Equal(when) {
		t.Errorf("scheduled_at = %v, error %v, want %v", scheduled, err, when)
	}
}

func TestOutboxMessageIsWrittenWithItsSuite(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.Outbox(ctx, governance.OutboxMessage{Key: "k1", Kind: "probe", Payload: queueObject})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.scalar("SELECT project_id || '/' || suite_id || ' ' || state || ' ' || attempts || ' ' || payload::text FROM outbox WHERE key='k1'"); got != `project/suite pending 0 {"suite": "suite"}` {
		t.Errorf("outbox row = %q", got)
	}
}

// A unit of work that fails while committing leaves no job and no message.
func TestFailedCommitLeavesNoQueuedWork(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite)
	for _, statement := range []string{
		`CREATE FUNCTION fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit refused' USING ERRCODE = '23514'; END; $$`,
		`CREATE CONSTRAINT TRIGGER outbox_fail_commit AFTER INSERT ON outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_commit()`,
	} {
		schemaExec(t, w.db.conn, statement)
	}

	err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		if err := tx.Enqueue(ctx, governance.Job{Kind: "probe", Args: queueObject}); err != nil {
			return err
		}
		return tx.Outbox(ctx, governance.OutboxMessage{Key: "k1", Kind: "probe", Payload: queueObject})
	})
	if err == nil {
		t.Fatal("Do reported success although the commit failed")
	}
	if got := w.scalar("SELECT (SELECT count(*) FROM river_job) || ' ' || (SELECT count(*) FROM outbox)"); got != "0 0" {
		t.Fatalf("jobs and messages after the failed commit = %q, want none", got)
	}
}

func TestEnqueueSystemWritesAJobAndAMessageWithoutASuite(t *testing.T) {
	w := newPGWorld(t)
	id, err := w.store.EnqueueSystem(t.Context(), governance.Job{Kind: "probe", Args: queueObject}, governance.OutboxMessage{Key: "probe-1", Kind: "probe", Payload: queueObject})
	if err != nil || id == 0 {
		t.Fatalf("EnqueueSystem = %d, %v, want a job id", id, err)
	}
	if got := w.scalar("SELECT id::text || ' ' || kind FROM river_job"); got != (strconv.FormatInt(id, 10) + " probe") {
		t.Errorf("job row = %q, want id %d", got, id)
	}
	if got := w.scalar("SELECT (project_id IS NULL AND suite_id IS NULL)::text || ' ' || kind FROM outbox WHERE key='probe-1'"); got != "true probe" {
		t.Errorf("outbox row = %q, want a message with no Suite", got)
	}
	status, err := w.store.SystemStatus(t.Context(), id, "probe-1")
	if err != nil || status != (postgres.SystemStatus{JobState: "available", OutboxState: "pending"}) {
		t.Fatalf("SystemStatus = %+v, %v", status, err)
	}
	if _, err := w.store.SystemStatus(t.Context(), id+1000, "probe-1"); !errors.Is(err, governance.ErrNotFound) {
		t.Errorf("unknown job = %v, want ErrNotFound", err)
	}
	if _, err := w.store.SystemStatus(t.Context(), id, "missing"); !errors.Is(err, governance.ErrNotFound) {
		t.Errorf("unknown message = %v, want ErrNotFound", err)
	}
}

func TestEnqueueSystemIsAtomic(t *testing.T) {
	w := newPGWorld(t)
	job := governance.Job{Kind: "probe", Args: queueObject}
	message := governance.OutboxMessage{Key: "probe-1", Kind: "probe", Payload: queueObject}
	if _, err := w.store.EnqueueSystem(t.Context(), job, message); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.EnqueueSystem(t.Context(), job, message); !errors.Is(err, governance.ErrOperationConflict) {
		t.Errorf("repeated key = %v, want ErrOperationConflict", err)
	}
	if _, err := w.store.EnqueueSystem(t.Context(), governance.Job{Kind: "Bad"}, governance.OutboxMessage{Key: "probe-2", Kind: "probe", Payload: queueObject}); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Errorf("invalid job = %v, want ErrInvalidRequest", err)
	}
	if _, err := w.store.EnqueueSystem(t.Context(), job, governance.OutboxMessage{Kind: "probe", Payload: queueObject}); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Errorf("invalid message = %v, want ErrInvalidRequest", err)
	}
	if got := w.scalar("SELECT (SELECT count(*) FROM river_job) || ' ' || (SELECT count(*) FROM outbox)"); got != "1 1" {
		t.Fatalf("jobs and messages = %q, want only those of the first success", got)
	}
}

// outboxFixture inserts due system messages directly, with their attempt counters.
func (w *pgWorld) outboxFixture(key string, attempts int, nextAttempt time.Time) {
	w.t.Helper()
	schemaExec(w.t, w.db.conn, "INSERT INTO outbox (key, kind, payload, attempts, next_attempt_at) VALUES ($1, 'probe', '{}', $2, $3)", key, attempts, nextAttempt)
}

func (w *pgWorld) outboxRow(key string) string {
	w.t.Helper()
	return w.scalar("SELECT state || ' ' || attempts || ' ' || coalesce(last_error, '-') FROM outbox WHERE key=$1", key)
}

func claimedKeys(claims []postgres.OutboxClaim) []string {
	keys := make([]string, len(claims))
	for i, claim := range claims {
		keys[i] = claim.Key
	}
	slices.Sort(keys)
	return keys
}

func TestClaimOutboxLeasesDueMessages(t *testing.T) {
	w := newPGWorld(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	w.outboxFixture("due-1", 0, now.Add(-time.Minute))
	w.outboxFixture("due-2", 2, now)
	w.outboxFixture("later", 0, now.Add(time.Second))
	params := postgres.ClaimOutboxParams{Now: now, Lease: time.Minute, MaxAttempts: 8, Limit: 10}

	claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), params))
	if err != nil {
		t.Fatal(err)
	}
	if got := claimedKeys(claims); !slices.Equal(got, []string{"due-1", "due-2"}) {
		t.Fatalf("claimed %v, want the two due messages", got)
	}
	for _, claim := range claims {
		wantAttempts := map[string]int{"due-1": 1, "due-2": 3}[claim.Key]
		if claim.Attempts != wantAttempts || claim.Kind != "probe" || string(claim.Payload) != "{}" {
			t.Errorf("claim %+v, want attempt %d of a probe with the stored payload", claim, wantAttempts)
		}
	}
	if again, err := claimsOf(w.store.ClaimOutbox(t.Context(), params)); err != nil || len(again) != 0 {
		t.Fatalf("claim inside the lease = %v, %v, want nothing", again, err)
	}

	params.Now = now.Add(time.Minute) // the lease of the crashed first claim has expired
	claims, err = claimsOf(w.store.ClaimOutbox(t.Context(), params))
	if err != nil {
		t.Fatal(err)
	}
	if got := claimedKeys(claims); !slices.Equal(got, []string{"due-1", "due-2", "later"}) {
		t.Fatalf("claimed %v after the lease, want the unfinished messages again", got)
	}
	if got := w.outboxRow("due-1"); got != "pending 2 -" {
		t.Errorf("redelivered row = %q, want pending with 2 attempts", got)
	}
}

func TestClaimOutboxHonorsTheLimitAndDueOrder(t *testing.T) {
	w := newPGWorld(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	w.outboxFixture("c", 0, now.Add(-time.Second))
	w.outboxFixture("a", 0, now.Add(-3*time.Second))
	w.outboxFixture("b", 0, now.Add(-2*time.Second))

	claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), postgres.ClaimOutboxParams{Now: now, Lease: time.Minute, MaxAttempts: 8, Limit: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if got := claimedKeys(claims); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("claimed %v, want the two longest due", got)
	}
}

func TestClaimOutboxFailsMessagesThatUsedEveryAttempt(t *testing.T) {
	w := newPGWorld(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	w.outboxFixture("poison", 3, now.Add(-time.Minute)) // claimed three times, never finished
	w.outboxFixture("fresh", 2, now.Add(-time.Minute))

	claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), postgres.ClaimOutboxParams{Now: now, Lease: time.Minute, MaxAttempts: 3, Limit: 10}))
	if err != nil {
		t.Fatal(err)
	}
	if got := claimedKeys(claims); !slices.Equal(got, []string{"fresh"}) {
		t.Fatalf("claimed %v, want only the message with an attempt left", got)
	}
	if got := w.outboxRow("poison"); got != "failed 3 delivery abandoned: every attempt was claimed without an outcome" {
		t.Errorf("poison row = %q, want failed with the reason", got)
	}
}

func TestConcurrentClaimsNeverShareAMessage(t *testing.T) {
	w := newPGWorld(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 60 {
		w.outboxFixture(string(rune('a'+i/26))+string(rune('a'+i%26)), 0, now.Add(-time.Minute))
	}
	var (
		mu      sync.Mutex
		claimed []string
		wg      sync.WaitGroup
	)
	for range 6 {
		wg.Go(func() {
			for {
				claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), postgres.ClaimOutboxParams{Now: now, Lease: time.Hour, MaxAttempts: 8, Limit: 4}))
				if err != nil {
					t.Error(err)
					return
				}
				if len(claims) == 0 {
					return
				}
				mu.Lock()
				claimed = append(claimed, claimedKeys(claims)...)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	slices.Sort(claimed)
	if len(claimed) != 60 || len(slices.Compact(claimed)) != 60 {
		t.Fatalf("%d claims for 60 messages (%d distinct), want each exactly once", len(claimed), len(slices.Compact(claimed)))
	}
}

func TestFinishOutboxIsConditionalOnTheClaim(t *testing.T) {
	w := newPGWorld(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	params := postgres.ClaimOutboxParams{Now: now, Lease: time.Minute, MaxAttempts: 8, Limit: 1}
	claim := func() postgres.OutboxClaim {
		claims, err := claimsOf(w.store.ClaimOutbox(t.Context(), params))
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim = %v, %v", claims, err)
		}
		return claims[0]
	}

	t.Run("delivered", func(t *testing.T) {
		w.outboxFixture("m1", 0, now.Add(-time.Second))
		applied, err := w.store.FinishOutbox(t.Context(), claim(), postgres.OutboxUpdate{State: postgres.OutboxDelivered, FinishedAt: now})
		if err != nil || !applied {
			t.Fatalf("FinishOutbox = %v, %v, want applied", applied, err)
		}
		if got := w.outboxRow("m1"); got != "delivered 1 -" {
			t.Errorf("row = %q", got)
		}
	})
	t.Run("a stale claim updates nothing", func(t *testing.T) {
		w.outboxFixture("m2", 0, now.Add(-time.Second))
		stale := claim()
		params.Now = now.Add(2 * time.Minute) // the lease expires and another relay claims again
		fresh := claim()
		if fresh.Key != "m2" || fresh.Attempts != 2 {
			t.Fatalf("reclaim = %+v, want attempt 2 of m2", fresh)
		}
		applied, err := w.store.FinishOutbox(t.Context(), stale, postgres.OutboxUpdate{State: postgres.OutboxDelivered, FinishedAt: now})
		if err != nil || applied {
			t.Fatalf("late outcome = %v, %v, want not applied and no error", applied, err)
		}
		if got := w.outboxRow("m2"); got != "pending 2 -" {
			t.Errorf("row = %q, want the newer claim untouched", got)
		}
		retryAt := now.Add(5 * time.Minute)
		applied, err = w.store.FinishOutbox(t.Context(), fresh, postgres.OutboxUpdate{State: postgres.OutboxPending, LastError: "boom", NextAttemptAt: retryAt})
		if err != nil || !applied {
			t.Fatalf("retry outcome = %v, %v, want applied", applied, err)
		}
		var next time.Time
		if err := w.pool.QueryRow(t.Context(), "SELECT next_attempt_at FROM outbox WHERE key='m2'").Scan(&next); err != nil || !next.Equal(retryAt) {
			t.Errorf("next attempt = %v, error %v, want %v", next, err, retryAt)
		}
		if got := w.outboxRow("m2"); got != "pending 2 boom" {
			t.Errorf("row = %q, want the error kept for the retry", got)
		}
	})
	t.Run("failed keeps the last error", func(t *testing.T) {
		params.Now = now.Add(time.Hour)
		w.outboxFixture("m3", 0, now.Add(-time.Second))
		applied, err := w.store.FinishOutbox(t.Context(), claim(), postgres.OutboxUpdate{State: postgres.OutboxFailed, LastError: "gave up", FinishedAt: now})
		if err != nil || !applied {
			t.Fatalf("FinishOutbox = %v, %v, want applied", applied, err)
		}
		if got := w.outboxRow("m3"); got != "failed 1 gave up" {
			t.Errorf("row = %q", got)
		}
	})
}

func TestQueueEntryPointsFailOnACanceledContext(t *testing.T) {
	w := newPGWorld(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	job := governance.Job{Kind: "probe", Args: queueObject}
	message := governance.OutboxMessage{Key: "k1", Kind: "probe", Payload: queueObject}
	if _, err := w.store.EnqueueSystem(ctx, job, message); !errors.Is(err, context.Canceled) {
		t.Errorf("EnqueueSystem = %v, want context.Canceled", err)
	}
	if _, err := w.store.SystemStatus(ctx, 1, "k1"); !errors.Is(err, context.Canceled) {
		t.Errorf("SystemStatus = %v, want context.Canceled", err)
	}
	if _, err := w.store.ClaimOutbox(ctx, postgres.ClaimOutboxParams{Now: time.Now(), Lease: time.Minute, MaxAttempts: 1, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Errorf("ClaimOutbox = %v, want context.Canceled", err)
	}
	if _, err := w.store.FinishOutbox(ctx, postgres.OutboxClaim{Key: "k1", Attempts: 1}, postgres.OutboxUpdate{State: postgres.OutboxDelivered, FinishedAt: time.Now()}); !errors.Is(err, context.Canceled) {
		t.Errorf("FinishOutbox = %v, want context.Canceled", err)
	}
	if got := w.scalar("SELECT (SELECT count(*) FROM river_job) || ' ' || (SELECT count(*) FROM outbox)"); got != "0 0" {
		t.Errorf("jobs and messages = %q, want none", got)
	}
}

func TestEnqueueSystemFailsWhenTheCommitFails(t *testing.T) {
	w := newPGWorld(t)
	for _, statement := range []string{
		`CREATE FUNCTION fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit refused' USING ERRCODE = '23514'; END; $$`,
		`CREATE CONSTRAINT TRIGGER outbox_fail_commit AFTER INSERT ON outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_commit()`,
	} {
		schemaExec(t, w.db.conn, statement)
	}
	_, err := w.store.EnqueueSystem(t.Context(), governance.Job{Kind: "probe", Args: queueObject}, governance.OutboxMessage{Key: "k1", Kind: "probe", Payload: queueObject})
	if err == nil {
		t.Fatal("EnqueueSystem reported success although the commit failed")
	}
	if got := w.scalar("SELECT (SELECT count(*) FROM river_job) || ' ' || (SELECT count(*) FROM outbox)"); got != "0 0" {
		t.Fatalf("jobs and messages = %q, want none", got)
	}
}

// claimsOf unwraps the claims of a claim pass.
func claimsOf(result postgres.ClaimOutboxResult, err error) ([]postgres.OutboxClaim, error) {
	return result.Claims, err
}
