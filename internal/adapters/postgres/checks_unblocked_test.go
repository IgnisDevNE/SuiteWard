//go:build integration

package postgres_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// A row written before migration 00021 names no unblocked, and reads false: the owner had no lift to record.
func TestCheckRowFromBeforeTheMigrationReadsNotUnblocked(t *testing.T) {
	w := newPGWorld(t)
	mustInsert(t, w.pool, "outbox", map[string]any{"key": "check:10:1:1"})
	mustInsert(t, w.pool, "pr_checks", nil) // the default row has no unblocked column: the column's default stands in for the old row

	record, found, err := w.store.Checks().Current(t.Context(), 10, 1)
	if err != nil || !found || record.Unblocked {
		t.Fatalf("Current = %+v, found %v, error %v; want the row, not unblocked", record, found, err)
	}
	same := record.Result
	same.Unblocked = true // nothing but the lift changes
	build := func(generation int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: fmt.Sprintf("check:10:1:%d", generation), Kind: "github_check", Payload: json.RawMessage(`{}`)}, nil
	}
	published, written, err := w.store.Checks().Publish(t.Context(), same, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), build)
	if err != nil || !written || published.Generation != 2 || !published.Unblocked {
		t.Fatalf("Publish = %+v, written %v, error %v; want generation 2, unblocked", published, written, err)
	}
	var stored bool
	if err := w.pool.QueryRow(t.Context(), "SELECT unblocked FROM pr_checks WHERE repository_id = 10 AND pull_number = 1").Scan(&stored); err != nil || !stored {
		t.Fatalf("stored unblocked = %v, error %v; want true", stored, err)
	}
	var generation int64
	if err := w.pool.QueryRow(t.Context(), "SELECT generation FROM pr_checks").Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("stored generation = %d, error %v; want 2", generation, err)
	}
}

func TestCheckLedgerHasTheUnblockedColumnAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 21 {
		t.Fatalf("SupportedVersion = %d; pr_checks.unblocked is migration 21", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var nullable, defaultsFalse bool
	if err := database.conn.QueryRow(t.Context(), `SELECT is_nullable = 'YES', column_default = 'false' FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'pr_checks' AND column_name = 'unblocked'`, database.schema).Scan(&nullable, &defaultsFalse); err != nil || nullable || !defaultsFalse {
		t.Fatalf("pr_checks.unblocked: nullable %v, default false %v, error %v; want NOT NULL DEFAULT false", nullable, defaultsFalse, err)
	}
}
