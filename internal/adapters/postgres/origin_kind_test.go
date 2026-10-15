//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestOriginKindExistsAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 16 {
		t.Fatalf("SupportedVersion = %d; the origin kind is migration 16", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var column string
	if err := database.conn.QueryRow(t.Context(), `SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'proposal_revisions' AND column_name = 'origin_kind'`, database.schema).Scan(&column); err != nil || column != "NO" {
		t.Fatalf("origin_kind nullable = %q, error %v; want a NOT NULL column", column, err)
	}
}

// seedVersion15 writes a Suite and the proposal revisions of a database at version 15, which has no origin_kind.
func seedVersion15(t *testing.T, connection *pgx.Conn, revisions ...map[string]any) {
	t.Helper()
	seedRows(t, connection, func(executor sqlExecutor) {
		proposals := map[any]bool{}
		for _, revision := range revisions {
			id, carrier := revision["proposal_id"], revision["carrier_id"]
			if !proposals[id] {
				proposals[id] = true
				mustInsert(t, executor, "proposals", map[string]any{"proposal_id": id, "carrier_id": carrier})
			}
			row := map[string]any{"origin_kind": omitColumn{}}
			for column, value := range revision {
				row[column] = value
			}
			mustInsert(t, executor, "proposal_revisions", row)
		}
	})
}

// A database at version 15 keeps its revisions: a baseline (no expected canonical) becomes a branch revision, a change a pull one,
// and the immutability trigger that the back-fill had to step around is on again afterwards.
func TestOriginKindMigrationBackfillsRevisions(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 15); err != nil {
		t.Fatal(err)
	}
	seedVersion15(t, database.conn,
		map[string]any{"proposal_id": "p1", "carrier_id": "pr-1", "revision_id": "r1", "seq": 1},
		map[string]any{"proposal_id": "p1", "carrier_id": "pr-1", "revision_id": "r2", "seq": 2},
		map[string]any{"proposal_id": "p2", "carrier_id": "pr-2", "revision_id": "r1", "seq": 1, "expected_version_id": "v1"},
		map[string]any{"proposal_id": "p2", "carrier_id": "pr-2", "revision_id": "r2", "seq": 2, "expected_version_id": "v1"},
	)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	rows, err := database.conn.Query(t.Context(), "SELECT proposal_id || '/' || revision_id, origin_kind FROM proposal_revisions")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, kind string
		if err := rows.Scan(&key, &kind); err != nil {
			t.Fatal(err)
		}
		kinds[key] = kind
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"p1/r1": "branch", "p1/r2": "branch", "p2/r1": "pull", "p2/r2": "pull"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for key, kind := range want {
		if kinds[key] != kind {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	rows.Close()
	_, err = database.conn.Exec(t.Context(), "UPDATE proposal_revisions SET origin_kind = 'branch'")
	schemaRequireError(t, err, "23514", "immutable_history")
}

// A proposal never changes kind, so a proposal that was rebound from no canonical onto a version cannot be represented:
// the migration refuses it, and rolls back with the immutability trigger still on.
func TestOriginKindMigrationRefusesAProposalOfBothKinds(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 15); err != nil {
		t.Fatal(err)
	}
	seedVersion15(t, database.conn,
		map[string]any{"proposal_id": "p1", "carrier_id": "pr-1", "revision_id": "r1", "seq": 1},
		map[string]any{"proposal_id": "p1", "carrier_id": "pr-1", "revision_id": "r2", "seq": 2, "expected_version_id": "v1"},
	)
	if err := migrations.Up(t.Context(), database.url); err == nil || !strings.Contains(err.Error(), "both kinds") {
		t.Fatalf("migration of a proposal with a baseline and a change revision = %v; want a refusal naming both kinds", err)
	}
	var column bool
	var version int64
	if err := database.conn.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'proposal_revisions' AND column_name = 'origin_kind'),
		(SELECT max(version_id) FROM goose_db_version WHERE is_applied)`, database.schema).Scan(&column, &version); err != nil || column || version != 15 {
		t.Fatalf("after the refusal origin_kind exists = %v, schema version %d, error %v; want it rolled back at 15", column, version, err)
	}
	_, err := database.conn.Exec(t.Context(), "UPDATE proposal_revisions SET origin = 'other'")
	schemaRequireError(t, err, "23514", "immutable_history")
}

func TestOriginKindConstraint(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedRows(t, database.conn, func(executor sqlExecutor) { mustInsert(t, executor, "proposals", nil) })
	for _, kind := range []any{"tag", "", "BRANCH", nil} {
		code, constraint := "23514", "proposal_revisions_origin_kind_check"
		if kind == nil {
			code, constraint = "23502", ""
		}
		schemaRequireError(t, insertRow(t.Context(), database.conn, "proposal_revisions", map[string]any{"origin_kind": kind}), code, constraint)
	}
	mustInsert(t, database.conn, "proposal_revisions", map[string]any{"origin_kind": "branch"})
	mustInsert(t, database.conn, "proposal_revisions", map[string]any{"revision_id": "r2", "seq": 2, "origin_kind": "pull"})
}

// seedRows writes the Suite, its policy and its version together with rows, in one transaction: the Suite's current
// version is a deferred foreign key.
func seedRows(t *testing.T, connection *pgx.Conn, rows func(sqlExecutor)) {
	t.Helper()
	transaction, err := connection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
	for _, table := range []string{"policies", "suites", "suite_versions"} {
		mustInsert(t, transaction, table, nil)
	}
	rows(transaction)
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
