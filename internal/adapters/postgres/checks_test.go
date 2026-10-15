//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks/checkstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

const checkHead = "0123456789abcdef0123456789abcdef01234567"

func init() {
	governanceRows["pr_checks"] = map[string]any{"repository_id": 10, "pull_number": 1, "head_sha": checkHead, "head_since": "2026-10-14T12:00:00Z", "state": "in_progress",
		"reason": "approval-missing", "reference": "", "change": "", "revision": 0, "command_id": 0, "generation": 1, "message_key": "check:10:1:1", "decided_at": "2026-10-14T12:00:00Z"}
}

func TestChecksConformance(t *testing.T) {
	checkstest.RunConformance(t, func(t *testing.T) checkstest.Env {
		w := newPGWorld(t)
		return checkstest.Env{Store: w.store.Checks(), OutboxKeys: func(ctx context.Context) ([]string, error) {
			rows, err := w.pool.Query(ctx, "SELECT key FROM outbox ORDER BY key COLLATE \"C\"")
			if err != nil {
				return nil, err
			}
			return pgx.CollectRows(rows, pgx.RowTo[string])
		}, FailMessage: func(ctx context.Context, key string, finishedAt time.Time) error {
			_, err := w.pool.Exec(ctx, "UPDATE outbox SET state = 'failed', finished_at = $2 WHERE key = $1", key, finishedAt)
			return err
		}}
	})
}

func TestCheckTableExistsAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 14 {
		t.Fatalf("SupportedVersion = %d; the check ledger is migration 14", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('pr_checks') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("pr_checks present = %v, error %v", present, err)
	}
}

func TestCheckConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, constraint string
		row              map[string]any
	}{
		{"repository zero", "pr_checks_repository_check", map[string]any{"repository_id": 0}},
		{"pull zero", "pr_checks_pull_check", map[string]any{"pull_number": 0}},
		{"short head", "pr_checks_head_check", map[string]any{"head_sha": "abc"}},
		{"uppercase head", "pr_checks_head_check", map[string]any{"head_sha": "0123456789ABCDEF0123456789abcdef01234567"}},
		{"empty reason", "pr_checks_reason_check", map[string]any{"reason": ""}},
		{"blank reason", "pr_checks_reason_check", map[string]any{"reason": " "}},
		{"unknown state", "pr_checks_state_check", map[string]any{"state": "neutral"}},
		{"reference", "pr_checks_reference_check", map[string]any{"reference": "P1"}},
		{"revision", "pr_checks_revision_check", map[string]any{"revision": -1}},
		{"command", "pr_checks_command_check", map[string]any{"command_id": -1}},
		{"generation zero", "pr_checks_generation_check", map[string]any{"generation": 0}},
		{"blank message key", "pr_checks_message_key_check", map[string]any{"message_key": " "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_checks", test.row), "23514", test.constraint)
		})
	}
	t.Run("one row per pull", func(t *testing.T) {
		mustInsert(t, database.conn, "pr_checks", nil)
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_checks", nil), "23505", "pr_checks_pkey")
	})
}

func TestCheckRowsAreUpdatedInPlaceNeverDeleted(t *testing.T) {
	w := newPGWorld(t)
	mustInsert(t, w.pool, "outbox", map[string]any{"key": "check:10:1:1"})
	mustInsert(t, w.pool, "pr_checks", nil)
	if _, err := w.pool.Exec(t.Context(), "UPDATE pr_checks SET state = 'success', reason = 'ready'"); err != nil {
		t.Fatalf("an update in place failed: %v", err)
	}
	for _, statement := range []string{"DELETE FROM pr_checks", "TRUNCATE pr_checks"} {
		_, err := w.pool.Exec(t.Context(), statement)
		schemaRequireError(t, err, "23514", "immutable_history")
	}
	if ownerCount(w, "SELECT count(*) FROM pr_checks") != 1 {
		t.Fatal("a rejected statement changed the rows")
	}
}

func TestPublishFailsWhenTheCurrentMessageIsMissing(t *testing.T) {
	w := newPGWorld(t)
	mustInsert(t, w.pool, "pr_checks", nil) // its message check:10:1:1 was never written
	r := checks.Result{RepositoryID: 10, PullNumber: 1, HeadSHA: checkHead, State: checks.InProgress, Reason: "approval-missing"}
	_, _, err := w.store.Checks().Publish(t.Context(), r, time.Now(), func(int64) (governance.OutboxMessage, error) {
		t.Fatal("the message was built for a ledger that is inconsistent")
		return governance.OutboxMessage{}, nil
	})
	if err == nil {
		t.Fatal("Publish accepted a record whose message is not in the outbox")
	}
}

func TestPublishDoesNotRetryADeliveredMessage(t *testing.T) {
	w := newPGWorld(t)
	r := checks.Result{RepositoryID: 10, PullNumber: 1, HeadSHA: checkHead, State: checks.InProgress, Reason: "approval-missing"}
	build := func(generation int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: fmt.Sprintf("check:10:1:%d", generation), Kind: "github_check", Payload: json.RawMessage(`{}`)}, nil
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if _, _, err := w.store.Checks().Publish(t.Context(), r, at, build); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(t.Context(), "UPDATE outbox SET state = 'delivered', finished_at = $1 WHERE key = 'check:10:1:1'", at); err != nil {
		t.Fatal(err)
	}

	record, written, err := w.store.Checks().Publish(t.Context(), r, at.Add(time.Hour), build) // long past the cooldown

	if err != nil || written || record.Generation != 1 {
		t.Fatalf("Publish = generation %d, written %v, error %v; want generation 1, no message", record.Generation, written, err)
	}
}
