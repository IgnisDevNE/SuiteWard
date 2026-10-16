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

// A database at version 16 refuses the new dispositions and keeps its rows; migration 17 accepts them, still refuses an unknown
// one, and leaves the operation rule as it was.
func TestBootstrapCommandMigrationExtendsTheDispositionCheck(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 16); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 1, "disposition": "malformed"})
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 2, "disposition": "proposed"}), "23514", "pr_commands_disposition_check")
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM pr_commands WHERE comment_id = 1 AND disposition = 'malformed'").Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("rows kept through the migration = %d, error %v; want the malformed one", kept, err)
	}
	for i, disposition := range []string{"proposed", "propose-refused", "skipped", "skip-refused"} {
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 10 + i, "disposition": disposition})
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 20 + i, "disposition": disposition, "operation_id": "op"}),
			"23514", "pr_commands_operation_check")
	}
	schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", map[string]any{"comment_id": 30, "disposition": "proposals"}), "23514", "pr_commands_disposition_check")
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 31, "disposition": "processed", "operation_id": "op"})
}
