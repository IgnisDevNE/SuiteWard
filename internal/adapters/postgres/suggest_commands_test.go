//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestSuggestCommandDispositionsExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 19 {
		t.Fatalf("SupportedVersion = %d; the suggested and suggest-refused dispositions are migration 19", migrations.SupportedVersion)
	}
}

// A database at version 18 refuses the new dispositions and keeps its rows; migration 19 accepts them without an operation id,
// still refuses an unknown one, and leaves the dispositions of 17 as they were.
func TestSuggestCommandMigrationExtendsTheDispositionCheck(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 18); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 1, "disposition": "skip-refused"})
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 2, "disposition": "suggested"}), "23514", "pr_commands_disposition_check")
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM pr_commands WHERE comment_id = 1 AND disposition = 'skip-refused'").Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("rows kept through the migration = %d, error %v; want the skip-refused one", kept, err)
	}
	for i, disposition := range []string{"suggested", "suggest-refused", "proposed", "skipped"} {
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 10 + i, "disposition": disposition})
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 20 + i, "disposition": disposition, "operation_id": "op"}),
			"23514", "pr_commands_operation_check")
	}
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 30, "disposition": "suggestions"}), "23514", "pr_commands_disposition_check")
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 31, "disposition": "processed", "operation_id": "op"})
}
