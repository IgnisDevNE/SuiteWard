//go:build integration

package postgres_test

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/pgtest"
)

// The dispositions of pr_commands at version 20.
var dispositionsAtVersion20 = []string{"not-command", "before-enrollment", "no-suite", "edited-first", "failed", "ignored-author", "malformed", "invalid-code", "replayed-code",
	"unknown-proposal", "processed", "proposed", "propose-refused", "skipped", "skip-refused", "suggested", "suggest-refused", "other-pull"}

// A database at version 20 keeps its check ledger and its command ledger: the check reads as not unblocked, every command has no mode and still
// reads, the insert-only ledger still refuses an update and a delete, and the check ledger is still updated in place and never deleted.
func TestExecutionMigrationKeepsTheCheckAndCommandLedgers(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 20); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "pr_checks", nil)
	for i, disposition := range dispositionsAtVersion20 {
		row := map[string]any{"comment_id": 100 + i, "disposition": disposition}
		if disposition == "processed" {
			row["operation_id"] = "op"
		}
		mustInsert(t, database.conn, "pr_commands", row)
	}
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}

	var unblocked bool
	var state, reason string
	if err := database.conn.QueryRow(t.Context(), "SELECT unblocked, state, reason FROM pr_checks").Scan(&unblocked, &state, &reason); err != nil || unblocked || state != "in_progress" || reason != "approval-missing" {
		t.Fatalf("migrated check: unblocked %v, state %q, reason %q, error %v; want the row as it was and not unblocked", unblocked, state, reason, err)
	}
	var commands, withMode int
	var kept []string
	if err := database.conn.QueryRow(t.Context(), "SELECT count(*), count(mode), array_agg(disposition ORDER BY comment_id) FROM pr_commands").Scan(&commands, &withMode, &kept); err != nil {
		t.Fatal(err)
	}
	if commands != len(dispositionsAtVersion20) || withMode != 0 || !slices.Equal(kept, dispositionsAtVersion20) {
		t.Fatalf("migrated commands: %d rows, %d with a mode, dispositions %v; want %d rows, none with a mode, each disposition as it was", commands, withMode, kept, len(dispositionsAtVersion20))
	}

	// The triggers still stand: pr_commands refuses an update and a delete, pr_checks a delete and a truncate, and pr_checks is still updated in place.
	for _, statement := range []string{"UPDATE pr_commands SET disposition = 'processed', operation_id = 'x'", "DELETE FROM pr_commands", "DELETE FROM pr_checks", "TRUNCATE pr_checks"} {
		_, err := database.conn.Exec(t.Context(), statement)
		schemaRequireError(t, err, "23514", "immutable_history")
	}
	if _, err := database.conn.Exec(t.Context(), "UPDATE pr_checks SET state = 'success', reason = 'ready'"); err != nil {
		t.Fatalf("an update of the check in place: %v", err)
	}

	// The stores read what the old version wrote, as the application role.
	content, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), pgtest.AppURL(t, database.url))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := postgres.NewStore(pool, content, testInserter())
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Checks().Current(t.Context(), 10, 1)
	if err != nil || !found || record.Unblocked || record.State != "success" || record.Generation != 1 {
		t.Fatalf("Current = %+v, found %v, error %v; want the migrated check, not unblocked", record, found, err)
	}
	latest, found, err := store.LatestOwnerCommand(t.Context(), 10, 1, 7, 0)
	if err != nil || !found || string(latest.Disposition) != "processed" || latest.Mode != "" || latest.Operation != "op" {
		t.Fatalf("LatestOwnerCommand = %+v, found %v, error %v; want the processed entry with no mode", latest, found, err)
	}
	if mode, found, err := store.PreviewMode(t.Context(), 10, 1, record.HeadSince); err != nil || found {
		t.Fatalf("PreviewMode = %q, found %v, error %v; want no preview mode", mode, found, err)
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
