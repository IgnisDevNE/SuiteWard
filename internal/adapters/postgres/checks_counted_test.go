//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// The canonical job whose counts an approval that is missing states (docs/contracts/runtime.md, "The canonical gate"; docs/contracts/persistence.md, "Changes to pr_checks"):
// migration 00022 adds pr_checks.counted_job, and a check from before it counts no job.

func TestCheckLedgerHasTheCountedJobColumnAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 22 {
		t.Fatalf("SupportedVersion = %d; pr_checks.counted_job is migration 22", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var nullable, defaultsZero bool
	var dataType string
	if err := database.conn.QueryRow(t.Context(), `SELECT is_nullable = 'YES', column_default = '0', data_type FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'pr_checks' AND column_name = 'counted_job'`, database.schema).Scan(&nullable, &defaultsZero, &dataType); err != nil || nullable || !defaultsZero || dataType != "bigint" {
		t.Fatalf("pr_checks.counted_job: nullable %v, default 0 %v, type %q, error %v; want bigint NOT NULL DEFAULT 0", nullable, defaultsZero, dataType, err)
	}
	mustInsert(t, database.conn, "pr_checks", map[string]any{"counted_job": 7})
	if _, err := database.conn.Exec(t.Context(), "UPDATE pr_checks SET counted_job = -1"); err == nil {
		t.Fatal("a negative counted job was stored")
	}
}

// A database at version 21 keeps its check ledger: the check reads as counting no job, and is still updated in place.
func TestCountedJobMigrationKeepsTheCheckLedger(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 21); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "pr_checks", nil)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var counted int64
	var reason string
	if err := database.conn.QueryRow(t.Context(), "SELECT counted_job, reason FROM pr_checks").Scan(&counted, &reason); err != nil || counted != 0 || reason != "approval-missing" {
		t.Fatalf("migrated check: counted job %d, reason %q, error %v; want the row as it was and no counted job", counted, reason, err)
	}
	if _, err := database.conn.Exec(t.Context(), "UPDATE pr_checks SET counted_job = 9"); err != nil {
		t.Fatalf("an update of the check in place: %v", err)
	}
}
