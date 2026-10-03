//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestPostgresSchemaFreshConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	tables := []string{"suites", "suite_versions", "operation_receipts", "consent_sources", "promotions", "audit_events", "publication_intents", "consent_acknowledgments"}
	var present int
	if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM pg_tables WHERE schemaname = $1 AND tablename = ANY($2)", database.schema, tables).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present != len(tables) {
		t.Fatalf("fresh migration created %d required tables; want %d", present, len(tables))
	}
	schemaExec(t, database.conn, "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','suite',0,'{}'),('project','other',0,'{}'),('project','maximum',18446744073709551615,'{}')")
	schemaExec(t, database.conn, "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','suite','version',$1,'{}')", schemaDigest)
	schemaExec(t, database.conn, "INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) VALUES ('consent','project','suite',4,'{}')")
	schemaExec(t, database.conn, "INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('source','project','suite','consent')")
	cases := []struct {
		name, statement, code, constraint string
		args                              []any
	}{
		{"duplicate Suite", "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','suite',0,'{}')", "23505", "suites_pkey", nil},
		{"duplicate version", "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','suite','version',$1,'{}')", "23505", "suite_versions_pkey", []any{schemaDigest}},
		{"global operation", "INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) VALUES ('consent','project','other',4,'{}')", "23505", "operation_receipts_pkey", nil},
		{"global source", "INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('source','project','suite','consent')", "23505", "consent_sources_pkey", nil},
		{"foreign Suite version", "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('foreign','suite','version',$1,'{}')", "23503", "suite_versions_suite_fkey", []any{schemaDigest}},
		{"wrong receipt scope", "INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('wrong-scope','project','other','consent')", "23503", "consent_sources_operation_fkey", nil},
		{"invalid digest", "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','suite','invalid','sha256:ABC','{}')", "23514", "suite_versions_digest_check", nil},
		{"payload is not object", "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','array',0,'[]')", "23514", "suites_payload_check", nil},
		{"revision exceeds uint64", "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','overflow',18446744073709551616,'{}')", "23514", "suites_revision_check", nil},
		{"negative revision", "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','negative',-1,'{}')", "23514", "suites_revision_check", nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.conn.Exec(t.Context(), test.statement, test.args...)
			schemaRequireError(t, err, test.code, test.constraint)
		})
	}
	var revision string
	if err := database.conn.QueryRow(t.Context(), "SELECT authority_revision::text FROM suites WHERE suite_id='maximum'").Scan(&revision); err != nil || revision != "18446744073709551615" {
		t.Fatalf("full uint64 revision round trip = %q, error %v", revision, err)
	}
}

func TestPostgresMigrationsLifecycle(t *testing.T) {
	t.Run("repeat and concurrent", func(t *testing.T) {
		database := newSchemaDatabase(t)
		var workers sync.WaitGroup
		failures := make(chan error, 4)
		for range 4 {
			workers.Go(func() { failures <- migrations.UpTo(t.Context(), database.url, 1) })
		}
		workers.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
			t.Fatal(err)
		}
		var applications int
		if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied").Scan(&applications); err != nil {
			t.Fatalf("concurrent migration did not establish version history: %v", err)
		}
		if applications != 1 {
			t.Fatalf("migration 1 applied %d times; want exactly one", applications)
		}
	})
	t.Run("failed body rolls back", func(t *testing.T) {
		database := newSchemaDatabase(t)
		schemaExec(t, database.conn, "CREATE TABLE suite_versions(conflicting_fixture boolean)")
		if err := migrations.UpTo(t.Context(), database.url, 1); err == nil {
			t.Fatal("conflicting preexisting relation did not reject migration")
		}
		var absent bool
		if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('suites') IS NULL").Scan(&absent); err != nil || !absent {
			t.Fatalf("failed migration left staged authority table: absent=%v error=%v", absent, err)
		}
		var applications int
		if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied").Scan(&applications); err != nil || applications != 0 {
			t.Fatalf("failed migration advanced version history: applications=%d error=%v", applications, err)
		}
		schemaExec(t, database.conn, "DROP TABLE suite_versions")
		if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
			t.Fatalf("failed migration cannot recover after its fixture is removed: %v", err)
		}
	})
	t.Run("downgrade rejected", func(t *testing.T) {
		database := newSchemaDatabase(t)
		if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
			t.Fatal(err)
		}
		if err := migrations.UpTo(t.Context(), database.url, 0); !errors.Is(err, migrations.ErrForwardOnly) {
			t.Fatalf("downgrade must fail closed; got %v", err)
		}
	})
}

func TestPostgresSchemaImmutableHistory(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
		t.Fatal(err)
	}
	seedSchemaHistory(t, database.conn)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"suite_versions", "operation_receipts", "consent_sources", "promotions", "audit_events", "publication_intents", "consent_acknowledgments"} {
		t.Run(table, func(t *testing.T) {
			for _, statement := range []string{
				"UPDATE " + table + " SET project_id=project_id",
				"DELETE FROM " + table,
				"TRUNCATE " + table + " CASCADE",
			} {
				_, err := database.conn.Exec(t.Context(), statement)
				if err == nil {
					t.Fatalf("immutable history mutation was accepted: %s", statement)
				}
				schemaRequireError(t, err, "23514", "immutable_history")
			}
		})
	}
	t.Run("authority requires exact next revision", func(t *testing.T) {
		for _, assignment := range []string{"governance_payload='{}'", "authority_revision=authority_revision+2"} {
			_, err := database.conn.Exec(t.Context(), "UPDATE suites SET "+assignment+" WHERE suite_id='suite'")
			schemaRequireError(t, err, "23514", "suites_revision_advance")
		}
		_, err := database.conn.Exec(t.Context(), "UPDATE suites SET suite_id='changed',authority_revision=authority_revision+1 WHERE suite_id='suite'")
		schemaRequireError(t, err, "23514", "suites_identity_immutable")
		schemaExec(t, database.conn, "UPDATE suites SET authority_revision=authority_revision+1,governance_payload='{\"updated\":true}' WHERE suite_id='suite'")
		_, err = database.conn.Exec(t.Context(), "UPDATE suites SET authority_revision=authority_revision+1 WHERE suite_id='maximum'")
		schemaRequireError(t, err, "23514", "suites_revision_check")
	})
	var versions, promotions int
	if err := database.conn.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM suite_versions),(SELECT count(*) FROM promotions)").Scan(&versions, &promotions); err != nil || versions != 1 || promotions != 1 {
		t.Fatalf("populated upgrade or rejected mutations lost history: versions=%d promotions=%d error=%v", versions, promotions, err)
	}
	if err := migrations.UpTo(t.Context(), database.url, 1); !errors.Is(err, migrations.ErrForwardOnly) {
		t.Fatalf("populated migration 2 cannot downgrade: %v", err)
	}
}

func seedSchemaHistory(t *testing.T, connection *pgx.Conn) {
	t.Helper()
	transaction, err := connection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(context.Background())
	schemaExec(t, transaction, "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','suite',0,'{}'),('project','maximum',18446744073709551615,'{}')")
	schemaExec(t, transaction, "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','suite','version',$1,'{}')", schemaDigest)
	schemaExec(t, transaction, "INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) VALUES ('promotion','project','suite',2,'{}'),('consent','project','suite',4,'{}')")
	schemaExec(t, transaction, "INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('source','project','suite','consent')")
	schemaExec(t, transaction, "INSERT INTO promotions(operation_id,project_id,suite_id,operation_kind,version_id,proposal_id,proposal_revision_id,carrier_id,source_revision,target_id,recorded_at,promotion_payload) VALUES ('promotion','project','suite',2,'version','proposal','revision','carrier','source','target',now(),'{}')")
	schemaExec(t, transaction, "INSERT INTO audit_events(operation_id,project_id,suite_id,event_kind,event_payload) VALUES ('promotion','project','suite',2,'{}'),('consent','project','suite',4,'{}')")
	schemaExec(t, transaction, "INSERT INTO publication_intents(operation_id,project_id,suite_id,publication_payload) VALUES ('promotion','project','suite','{}')")
	schemaExec(t, transaction, "INSERT INTO consent_acknowledgments(operation_id,project_id,suite_id,acknowledgment_payload) VALUES ('consent','project','suite','{}')")
	schemaExec(t, transaction, "UPDATE suites SET current_version_id='version',authority_revision=authority_revision+1 WHERE suite_id='suite'")
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

const schemaDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type schemaDatabase struct {
	url    string
	schema string
	conn   *pgx.Conn
}

func newSchemaDatabase(t *testing.T) schemaDatabase {
	t.Helper()
	connectionURL := os.Getenv("SUITEWARD_TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Fatal("SUITEWARD_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(t.Context(), connectionURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL schema fixture: %v", err)
	}
	schema := "sw_schema_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("remove owned schema: %v", err)
		}
		admin.Close(context.Background())
	})
	parsed, err := url.Parse(connectionURL)
	if err != nil {
		t.Fatal("fixture connection must be a PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	connectionURL = parsed.String()
	conn, err := pgx.Connect(t.Context(), connectionURL)
	if err != nil {
		t.Fatalf("connect owned schema: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return schemaDatabase{url: connectionURL, schema: schema, conn: conn}
}

func schemaExec(t *testing.T, connection interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, statement string, args ...any) {
	t.Helper()
	if _, err := connection.Exec(t.Context(), statement, args...); err != nil {
		t.Fatal(fmt.Errorf("schema fixture statement: %w", err))
	}
}

func schemaRequireError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code || (constraint != "" && postgresError.ConstraintName != constraint) {
		t.Fatalf("expected PostgreSQL %s/%s rejection; got %v", code, constraint, err)
	}
}
