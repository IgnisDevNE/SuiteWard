//go:build integration

package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// Migration 18 adds the cycle start to a database at version 17 and keeps its rows: a repository row from before
// the column has no start, which the store reads as the zero time.
func TestCycleStartMigrationAppliesAtVersion17AndOldRowsReadNoStart(t *testing.T) {
	if migrations.SupportedVersion < 18 {
		t.Fatalf("SupportedVersion = %d; the cycle start is migration 18", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 17); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "github_repositories", map[string]any{"repo": "acme/old", "last_success": "2026-10-02T12:00:00Z"})

	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatalf("migration 18 on a database at version 17: %v", err)
	}

	pool, err := pgxpool.New(t.Context(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	content, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewStore(pool, content, testInserter())
	if err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Repository(t.Context(), "acme/old")
	if err != nil || !found {
		t.Fatalf("Repository = %+v, %v, %v; want the row kept through the migration", state, found, err)
	}
	if !state.CycleStartedAt.IsZero() || state.LastSuccess.IsZero() {
		t.Fatalf("state = %+v; want the old success kept and no cycle start", state)
	}
}
