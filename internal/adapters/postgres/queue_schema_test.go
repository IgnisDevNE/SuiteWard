//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// River's own migrator must accept a database that goose migrated: one
// migrator and one readiness check own the schema (ADR 0026).
func TestRiverMigratorValidatesTheGooseSchema(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrator.Validate(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("River validation failed: %v", result.Messages)
	}
}

func TestSupportedVersionCoversTheOutbox(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('outbox') IS NOT NULL AND to_regclass('river_job') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("outbox and river_job present = %v, error %v; want both after migrating to SupportedVersion", present, err)
	}
	var highest int64
	if err := database.conn.QueryRow(t.Context(), "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&highest); err != nil || highest != migrations.SupportedVersion {
		t.Fatalf("highest goose version = %d, error %v; want SupportedVersion %d", highest, err, migrations.SupportedVersion)
	}
}

func insertOutbox(ctx context.Context, executor sqlExecutor, overrides map[string]any) error {
	return insertRow(ctx, executor, "outbox", overrides)
}

func TestOutboxConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	for _, test := range []struct {
		name, code, constraint string
		row                    map[string]any
	}{
		{"scope needs both or neither", "23514", "outbox_scope_check", map[string]any{"project_id": "project"}},
		{"unknown suite", "23503", "outbox_suite_fkey", map[string]any{"project_id": "project", "suite_id": "missing"}},
		{"empty key", "23514", "outbox_key_check", map[string]any{"key": ""}},
		{"overlong key", "23514", "outbox_key_check", map[string]any{"key": string(make([]byte, 201))}},
		{"bad kind", "23514", "outbox_kind_check", map[string]any{"kind": "Probe"}},
		{"payload is not an object", "23514", "outbox_payload_check", map[string]any{"payload": `[]`}},
		{"unknown state", "23514", "outbox_state_check", map[string]any{"state": "sent", "finished_at": "2026-10-02T12:00:00Z"}},
		{"negative attempts", "23514", "outbox_attempts_check", map[string]any{"attempts": -1}},
		{"terminal needs a finish time", "23514", "outbox_finished_check", map[string]any{"state": "delivered"}},
		{"pending has no finish time", "23514", "outbox_finished_check", map[string]any{"finished_at": "2026-10-02T12:00:00Z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := database.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
			schemaRequireError(t, insertOutbox(t.Context(), transaction, test.row), test.code, test.constraint)
		})
	}
	t.Run("scoped and system messages are accepted", func(t *testing.T) {
		if err := insertOutbox(t.Context(), database.conn, map[string]any{"key": "scoped", "project_id": "project", "suite_id": "suite"}); err != nil {
			t.Fatal(err)
		}
		if err := insertOutbox(t.Context(), database.conn, map[string]any{"key": "system"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("duplicate key", func(t *testing.T) {
		schemaRequireError(t, insertOutbox(t.Context(), database.conn, map[string]any{"key": "system"}), "23505", "outbox_pkey")
	})
}

func TestOutboxDeliveryStateMachine(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pending", "delivered", "failed"} {
		if err := insertOutbox(t.Context(), database.conn, map[string]any{"key": key}); err != nil {
			t.Fatal(err)
		}
	}
	schemaExec(t, database.conn, "UPDATE outbox SET attempts=attempts+1, next_attempt_at=now()+interval '1 minute' WHERE key='pending'")
	schemaExec(t, database.conn, "UPDATE outbox SET state='delivered', finished_at=now() WHERE key='delivered'")
	schemaExec(t, database.conn, "UPDATE outbox SET state='failed', last_error='boom', finished_at=now() WHERE key='failed'")
	for _, test := range []struct {
		name, statement, constraint string
	}{
		{"delivered is final", "UPDATE outbox SET state='pending', finished_at=NULL WHERE key='delivered'", "outbox_terminal_state"},
		{"failed is final", "UPDATE outbox SET last_error='other' WHERE key='failed'", "outbox_terminal_state"},
		{"payload is immutable", "UPDATE outbox SET payload='{\"a\":1}' WHERE key='pending'", "outbox_immutable"},
		{"kind is immutable", "UPDATE outbox SET kind='other' WHERE key='pending'", "outbox_immutable"},
		{"key is immutable", "UPDATE outbox SET key='moved' WHERE key='pending'", "outbox_immutable"},
		{"scope is immutable", "UPDATE outbox SET project_id='project', suite_id='suite' WHERE key='pending'", "outbox_immutable"},
		{"delete", "DELETE FROM outbox", "immutable_history"},
		{"truncate", "TRUNCATE outbox", "immutable_history"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.conn.Exec(t.Context(), test.statement)
			schemaRequireError(t, err, "23514", test.constraint)
		})
	}
	t.Run("pending moves to a terminal state", func(t *testing.T) {
		schemaExec(t, database.conn, "UPDATE outbox SET state='delivered', finished_at=now() WHERE key='pending'")
	})
}
