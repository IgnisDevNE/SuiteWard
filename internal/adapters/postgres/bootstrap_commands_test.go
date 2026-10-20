//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestBootstrapCommandDispositionsExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 17 {
		t.Fatalf("SupportedVersion = %d; the propose and skip dispositions are migration 17", migrations.SupportedVersion)
	}
}

// requireDispositionMigration checks a migration of the pr_commands disposition check: a database at version from refuses
// refused and keeps its row of kept; after the migration each of accepted is stored, none of them with an operation id,
// unknown is still refused, and an operation id stays required for processed.
func requireDispositionMigration(t *testing.T, from int64, kept, refused string, accepted []string, unknown string) {
	t.Helper()
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, from); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 1, "disposition": kept})
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 2, "disposition": refused}), "23514", "pr_commands_disposition_check")
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM pr_commands WHERE comment_id = 1 AND disposition = $1", kept).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows kept through the migration = %d, error %v; want the %s one", count, err, kept)
	}
	for i, disposition := range accepted {
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 10 + i, "disposition": disposition})
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 20 + i, "disposition": disposition, "operation_id": "op"}),
			"23514", "pr_commands_operation_check")
	}
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 30, "disposition": unknown}), "23514", "pr_commands_disposition_check")
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 31, "disposition": "processed", "operation_id": "op"})
}

// A database at version 16 refuses the new dispositions and keeps its rows; migration 17 accepts them, still refuses an unknown
// one, and leaves the operation rule as it was.
func TestBootstrapCommandMigrationExtendsTheDispositionCheck(t *testing.T) {
	requireDispositionMigration(t, 16, "malformed", "proposed", []string{"proposed", "propose-refused", "skipped", "skip-refused"}, "proposals")
}
