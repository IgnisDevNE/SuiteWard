//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

var governanceTables = []string{"suites", "policies", "suite_versions", "proposals", "proposal_revisions", "assessments", "consent_results", "operations", "promotions"}

// governanceRows are mutually consistent default rows; tests copy them with overrides.
var governanceRows = map[string]map[string]any{
	"policies":           {"project_id": "project", "revision_id": "policy-1", "owner_id": "owner", "owner_kind": "human"},
	"suites":             {"project_id": "project", "suite_id": "suite", "revision": 1, "current_version_id": "v1", "target_id": "main", "policy_revision_id": "policy-1"},
	"suite_versions":     {"project_id": "project", "suite_id": "suite", "version_id": "v1", "manifest_digest": schemaDigest, "manifest": `{"entries":[]}`},
	"proposals":          {"project_id": "project", "suite_id": "suite", "proposal_id": "p1", "carrier_id": "pr-1"},
	"proposal_revisions": {"project_id": "project", "suite_id": "suite", "proposal_id": "p1", "revision_id": "r1", "seq": 1, "origin": "origin-1", "carrier_id": "pr-1", "manifest_digest": schemaDigest, "scope_digest": schemaDigest, "covered_inputs": `{}`, "expected_version_id": nil, "policy_revision_id": "policy-1"},
	"assessments":        {"project_id": "project", "suite_id": "suite", "proposal_id": "p1", "revision_id": "r1", "source": "sha-1", "evidence_emitter": "ci", "evidence_source": "sha-1", "evidence_revision_id": "r1", "outcome": "passed"},
	"consent_results":    {"source_command_id": "src-1", "operation_id": "consent-op", "project_id": "project", "suite_id": "suite", "proposal_id": "p1", "revision_id": "r1", "actor_id": "owner", "actor_kind": "human", "carrier_id": "pr-1", "action": "approve", "command_order": 1, "outcome": "approved", "reason": "none"},
	"operations":         {"operation_id": "consent-op", "project_id": "project", "suite_id": "suite", "kind": "consent", "source_command_id": "src-1", "receipt": `{"kind":"consent"}`},
	"promotions":         {"operation_id": "promo-op", "project_id": "project", "suite_id": "suite", "version_id": "v1", "proposal_id": "p1", "revision_id": "r1", "carrier_id": "pr-1", "source_revision": "sha-1", "target_id": "main", "recorded_at": "2026-10-02T12:00:00Z", "corrects_version_id": nil},
}

type sqlExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// insertRow inserts the default row of table with overrides applied.
func insertRow(ctx context.Context, executor sqlExecutor, table string, overrides map[string]any) error {
	values := map[string]any{}
	for column, value := range governanceRows[table] {
		values[column] = value
	}
	for column, value := range overrides {
		values[column] = value
	}
	columns := make([]string, 0, len(values))
	for column := range values {
		columns = append(columns, column)
	}
	slices.Sort(columns)
	placeholders := make([]string, len(columns))
	arguments := make([]any, len(columns))
	for i, column := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		arguments[i] = values[column]
	}
	_, err := executor.Exec(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(placeholders, ", ")+")", arguments...)
	return err
}

func mustInsert(t *testing.T, executor sqlExecutor, table string, overrides map[string]any) {
	t.Helper()
	if err := insertRow(t.Context(), executor, table, overrides); err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
}

// seedGovernance writes one complete, consistent Suite history in one transaction.
func seedGovernance(t *testing.T, connection *pgx.Conn) {
	t.Helper()
	transaction, err := connection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(context.Background())
	for _, table := range []string{"policies", "suites", "suite_versions", "proposals", "proposal_revisions", "assessments", "consent_results", "operations", "promotions"} {
		mustInsert(t, transaction, table, nil)
	}
	mustInsert(t, transaction, "operations", map[string]any{"operation_id": "promo-op", "kind": "promote", "source_command_id": nil, "receipt": `{"kind":"promote"}`})
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestGovernanceSchemaFreshMigration(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	rows, err := database.conn.Query(t.Context(), "SELECT tablename FROM pg_tables WHERE schemaname = $1 ORDER BY tablename", database.schema)
	if err != nil {
		t.Fatal(err)
	}
	present, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"goose_db_version"}, governanceTables...)
	slices.Sort(want)
	if !slices.Equal(present, want) {
		t.Fatalf("fresh migration tables = %v; want exactly %v", present, want)
	}
	seedGovernance(t, database.conn)
}

func TestGovernanceMigrationsLifecycle(t *testing.T) {
	t.Run("repeat and concurrent", func(t *testing.T) {
		database := newSchemaDatabase(t)
		var workers sync.WaitGroup
		failures := make(chan error, 4)
		for range 4 {
			workers.Go(func() { failures <- migrations.Up(t.Context(), database.url) })
		}
		workers.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := migrations.Up(t.Context(), database.url); err != nil {
			t.Fatal(err)
		}
		var applications, later int
		if err := database.conn.QueryRow(t.Context(), "SELECT count(*) FILTER (WHERE version_id=1), count(*) FILTER (WHERE version_id>1) FROM goose_db_version WHERE is_applied").Scan(&applications, &later); err != nil || applications != 1 || later != 0 {
			t.Fatalf("migration 1 applied %d times, later migrations %d (error %v); want exactly one and none", applications, later, err)
		}
	})
	t.Run("failed body rolls back", func(t *testing.T) {
		database := newSchemaDatabase(t)
		schemaExec(t, database.conn, "CREATE TABLE suite_versions(conflicting_fixture boolean)")
		if err := migrations.Up(t.Context(), database.url); err == nil {
			t.Fatal("conflicting preexisting relation did not reject the migration")
		}
		var absent bool
		if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('suites') IS NULL").Scan(&absent); err != nil || !absent {
			t.Fatalf("failed migration left a partial schema: absent=%v error=%v", absent, err)
		}
		schemaExec(t, database.conn, "DROP TABLE suite_versions")
		if err := migrations.Up(t.Context(), database.url); err != nil {
			t.Fatalf("failed migration cannot recover after its fixture is removed: %v", err)
		}
	})
	t.Run("downgrade and newer database rejected", func(t *testing.T) {
		database := newSchemaDatabase(t)
		if err := migrations.Up(t.Context(), database.url); err != nil {
			t.Fatal(err)
		}
		if err := migrations.UpTo(t.Context(), database.url, 0); !errors.Is(err, migrations.ErrForwardOnly) {
			t.Fatalf("downgrade must fail closed; got %v", err)
		}
		schemaExec(t, database.conn, "INSERT INTO goose_db_version(version_id, is_applied) VALUES (2, true)")
		if err := migrations.Up(t.Context(), database.url); !errors.Is(err, migrations.ErrForwardOnly) {
			t.Fatalf("a database newer than the supported schema must be rejected; got %v", err)
		}
	})
}

func TestGovernanceImmutableHistory(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	for _, table := range []string{"policies", "suite_versions", "proposals", "proposal_revisions", "assessments", "consent_results", "operations", "promotions"} {
		t.Run(table, func(t *testing.T) {
			for _, statement := range []string{
				"UPDATE " + table + " SET project_id=project_id",
				"DELETE FROM " + table,
				"TRUNCATE " + table + " CASCADE",
			} {
				_, err := database.conn.Exec(t.Context(), statement)
				schemaRequireError(t, err, "23514", "immutable_history")
			}
		})
	}
	var versions, promotions, operations int
	if err := database.conn.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM suite_versions),(SELECT count(*) FROM promotions),(SELECT count(*) FROM operations)").Scan(&versions, &promotions, &operations); err != nil || versions != 1 || promotions != 1 || operations != 2 {
		t.Fatalf("rejected mutations lost history: versions=%d promotions=%d operations=%d error=%v", versions, promotions, operations, err)
	}
}

func TestGovernanceSuiteRevisionGuard(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	for _, test := range []struct{ name, statement, constraint string }{
		{"delete", "DELETE FROM suites", "immutable_history"},
		{"truncate", "TRUNCATE suites CASCADE", "immutable_history"},
		{"same revision", "UPDATE suites SET current_version_id=current_version_id", "suites_revision_advance"},
		{"revision skips", "UPDATE suites SET revision=revision+2", "suites_revision_advance"},
		{"revision rewinds", "UPDATE suites SET revision=revision-1", "suites_revision_advance"},
		{"suite key changes", "UPDATE suites SET suite_id='moved', revision=revision+1", "suites_identity_immutable"},
		{"project key changes", "UPDATE suites SET project_id='moved', revision=revision+1", "suites_identity_immutable"},
		{"target changes", "UPDATE suites SET target_id='other', revision=revision+1", "suites_governance_immutable"},
		{"policy changes", "UPDATE suites SET policy_revision_id='policy-2', revision=revision+1", "suites_governance_immutable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.conn.Exec(t.Context(), test.statement)
			schemaRequireError(t, err, "23514", test.constraint)
		})
	}
	schemaExec(t, database.conn, "UPDATE suites SET revision=revision+1")
	var revision int64
	if err := database.conn.QueryRow(t.Context(), "SELECT revision FROM suites").Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("exact next revision update: revision=%d error=%v", revision, err)
	}
}

func TestGovernanceReadIndexes(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for index, table := range map[string]string{"consent_results_proposal_seq_idx": "consent_results", "operations_source_command_idx": "operations"} {
		var present bool
		if err := database.conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 AND indexname = $3)", database.schema, table, index).Scan(&present); err != nil || !present {
			t.Fatalf("index %s on %s is missing (error %v)", index, table, err)
		}
	}
}

func TestGovernanceTruncateRejectedEverywhere(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, table := range governanceTables {
		t.Run(table, func(t *testing.T) {
			_, err := database.conn.Exec(t.Context(), "TRUNCATE "+table+" CASCADE")
			schemaRequireError(t, err, "23514", "immutable_history")
		})
	}
}

func TestGovernanceCurrentVersionPointerIsDeferred(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	t.Run("version and pointer in one transaction", func(t *testing.T) {
		transaction, err := database.conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback(context.Background())
		schemaExec(t, transaction, "UPDATE suites SET revision=revision+1, current_version_id='v2'")
		mustInsert(t, transaction, "suite_versions", map[string]any{"version_id": "v2"})
		if err := transaction.Commit(t.Context()); err != nil {
			t.Fatalf("deferred pointer must accept a version inserted later in the same transaction: %v", err)
		}
	})
	t.Run("dangling pointer rejected at commit", func(t *testing.T) {
		transaction, err := database.conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback(context.Background())
		schemaExec(t, transaction, "UPDATE suites SET revision=revision+1, current_version_id='ghost'")
		schemaRequireError(t, transaction.Commit(t.Context()), "23503", "suites_current_version_fkey")
		var current string
		if err := database.conn.QueryRow(t.Context(), "SELECT current_version_id FROM suites").Scan(&current); err != nil || current != "v2" {
			t.Fatalf("rejected pointer must not persist: current=%q error=%v", current, err)
		}
	})
	t.Run("pointer to a version of another Suite rejected", func(t *testing.T) {
		mustInsert(t, database.conn, "suites", map[string]any{"suite_id": "other", "revision": 0, "current_version_id": nil})
		mustInsert(t, database.conn, "suite_versions", map[string]any{"suite_id": "other", "version_id": "foreign"})
		transaction, err := database.conn.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback(context.Background())
		schemaExec(t, transaction, "UPDATE suites SET revision=revision+1, current_version_id='foreign' WHERE suite_id='suite'")
		schemaRequireError(t, transaction.Commit(t.Context()), "23503", "suites_current_version_fkey")
	})
}

// TestGovernanceConstraints runs each case in a rolled-back transaction over
// the seeded history plus a second Suite, version, revision and proposal.
func TestGovernanceConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"unknown principal kind", "policies", "23514", "policies_owner_kind_check", map[string]any{"revision_id": "bad", "owner_kind": "robot"}},
		{"empty identifier", "policies", "23514", "", map[string]any{"revision_id": " "}},
		{"negative revision", "suites", "23514", "suites_revision_check", map[string]any{"suite_id": "negative", "revision": -1, "current_version_id": nil}},
		{"unknown policy", "suites", "23503", "suites_policy_fkey", map[string]any{"suite_id": "nopolicy", "policy_revision_id": "missing", "current_version_id": nil}},
		{"invalid manifest digest", "suite_versions", "23514", "suite_versions_manifest_digest_check", map[string]any{"version_id": "bad", "manifest_digest": "sha256:ABC"}},
		{"manifest is not an object", "suite_versions", "23514", "suite_versions_manifest_check", map[string]any{"version_id": "array", "manifest": `[]`}},
		{"covered inputs is not an object", "proposal_revisions", "23514", "proposal_revisions_covered_inputs_check", map[string]any{"revision_id": "r9", "seq": 9, "covered_inputs": `"text"`}},
		{"revision seq must be positive", "proposal_revisions", "23514", "proposal_revisions_seq_check", map[string]any{"revision_id": "r9", "seq": 0}},
		{"duplicate revision seq", "proposal_revisions", "23505", "proposal_revisions_seq_key", map[string]any{"revision_id": "r9"}},
		{"revision of unknown proposal", "proposal_revisions", "23503", "proposal_revisions_proposal_fkey", map[string]any{"proposal_id": "missing", "revision_id": "r9", "seq": 9}},
		{"unknown integrity outcome", "assessments", "23514", "assessments_outcome_check", map[string]any{"source": "sha-2", "outcome": "green"}},
		{"evidence without outcome", "assessments", "23514", "assessments_evidence_check", map[string]any{"source": "sha-2", "outcome": nil}},
		{"outcome without evidence", "assessments", "23514", "assessments_evidence_check", map[string]any{"source": "sha-3", "evidence_emitter": nil, "evidence_source": nil, "evidence_revision_id": nil}},
		{"evidence without revision", "assessments", "23514", "assessments_evidence_check", map[string]any{"source": "sha-4", "evidence_revision_id": nil}},
		{"revision without evidence", "assessments", "23514", "assessments_evidence_check", map[string]any{"source": "sha-5", "evidence_emitter": nil, "evidence_source": nil, "outcome": nil}},
		{"evidence of an unknown revision", "assessments", "23503", "assessments_evidence_revision_fkey", map[string]any{"source": "sha-6", "evidence_revision_id": "missing"}},
		{"unknown consent action", "consent_results", "23514", "consent_results_action_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "action": "veto"}},
		{"unknown consent outcome", "consent_results", "23514", "consent_results_outcome_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "outcome": "maybe"}},
		{"unknown consent reason", "consent_results", "23514", "consent_results_reason_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "outcome": "rejected", "reason": "command_conflict"}},
		{"rejection needs a reason", "consent_results", "23514", "consent_results_reason_outcome_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "outcome": "rejected"}},
		{"reason requires rejection", "consent_results", "23514", "consent_results_reason_outcome_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "reason": "unauthorized"}},
		{"unknown actor kind", "consent_results", "23514", "consent_results_actor_kind_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "actor_kind": "robot"}},
		{"command order must be positive", "consent_results", "23514", "consent_results_command_order_check", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "command_order": 0}},
		{"global source command id", "consent_results", "23505", "consent_results_pkey", map[string]any{"operation_id": "op-2"}},
		{"unique result operation id", "consent_results", "23505", "consent_results_operation_key", map[string]any{"source_command_id": "src-2"}},
		{"result for unknown proposal", "consent_results", "23503", "consent_results_proposal_fkey", map[string]any{"source_command_id": "src-2", "operation_id": "op-2", "proposal_id": "missing"}},
		{"unknown operation kind", "operations", "23514", "operations_kind_check", map[string]any{"operation_id": "op-2", "kind": "merge", "source_command_id": nil}},
		{"consent operation needs a source command", "operations", "23514", "operations_source_check", map[string]any{"operation_id": "op-2", "source_command_id": nil}},
		{"promotion operation has no source command", "operations", "23514", "operations_source_check", map[string]any{"operation_id": "op-2", "kind": "promote"}},
		{"receipt is not an object", "operations", "23514", "operations_receipt_check", map[string]any{"operation_id": "op-2", "receipt": `[]`}},
		{"global operation id in another Suite", "operations", "23505", "operations_pkey", map[string]any{"suite_id": "other", "source_command_id": nil, "kind": "promote"}},
		{"unknown source command", "operations", "23503", "operations_source_fkey", map[string]any{"operation_id": "op-2", "source_command_id": "missing"}},
		{"source command in another Suite", "operations", "23503", "operations_source_fkey", map[string]any{"operation_id": "op-2", "suite_id": "other"}},
		{"duplicate promoted version", "promotions", "23505", "promotions_pkey", map[string]any{"operation_id": "promo-2", "revision_id": "r2"}},
		{"duplicate promoted reference", "promotions", "23505", "promotions_reference_key", map[string]any{"operation_id": "promo-2", "version_id": "v2"}},
		{"duplicate promotion operation", "promotions", "23505", "promotions_operation_key", map[string]any{"version_id": "v2", "revision_id": "r2"}},
		{"promotion of unknown version", "promotions", "23503", "promotions_version_fkey", map[string]any{"operation_id": "promo-2", "version_id": "missing", "revision_id": "r2"}},
		{"promotion of unknown revision", "promotions", "23503", "promotions_revision_fkey", map[string]any{"operation_id": "promo-2", "version_id": "v2", "revision_id": "missing"}},
		{"promotion corrects unknown version", "promotions", "23503", "promotions_corrects_fkey", map[string]any{"operation_id": "promo-2", "version_id": "v2", "revision_id": "r2", "corrects_version_id": "missing"}},
		{"promotion corrects itself", "promotions", "23514", "promotions_corrects_check", map[string]any{"operation_id": "promo-2", "version_id": "v2", "revision_id": "r2", "corrects_version_id": "v2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := database.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer transaction.Rollback(context.Background())
			mustInsert(t, transaction, "suites", map[string]any{"suite_id": "other", "revision": 0, "current_version_id": nil})
			mustInsert(t, transaction, "suite_versions", map[string]any{"version_id": "v2"})
			mustInsert(t, transaction, "proposals", map[string]any{"proposal_id": "p2", "carrier_id": "pr-2"})
			mustInsert(t, transaction, "proposal_revisions", map[string]any{"revision_id": "r2", "seq": 2})
			schemaRequireError(t, insertRow(t.Context(), transaction, test.table, test.row), test.code, test.constraint)
		})
	}
}

func TestGovernanceMissingEvidenceIsStoredAsNull(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	mustInsert(t, database.conn, "assessments", map[string]any{"source": "sha-missing", "evidence_emitter": nil, "evidence_source": nil, "evidence_revision_id": nil, "outcome": nil})
}

func TestGovernanceEvidenceMayCiteAnotherRevisionOfTheProposal(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	mustInsert(t, database.conn, "proposal_revisions", map[string]any{"revision_id": "r2", "seq": 2})
	mustInsert(t, database.conn, "assessments", map[string]any{"revision_id": "r2", "source": "sha-2", "evidence_source": "sha-1", "evidence_revision_id": "r1"})
	var cited string
	if err := database.conn.QueryRow(t.Context(), "SELECT evidence_revision_id FROM assessments WHERE revision_id = 'r2'").Scan(&cited); err != nil || cited != "r1" {
		t.Fatalf("stored evidence revision = %q, error %v; want r1", cited, err)
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
