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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
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
	t.Run("failed populated upgrade rolls back", func(t *testing.T) {
		database := newSchemaDatabase(t)
		if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
			t.Fatal(err)
		}
		seedSchemaHistory(t, database.conn)
		schemaExec(t, database.conn, "CREATE FUNCTION require_next_authority_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$")
		if err := migrations.Up(t.Context(), database.url); err == nil {
			t.Fatal("conflicting upgrade function did not reject migration 2")
		}
		var applied, versions int
		var firstFunctionAbsent bool
		if err := database.conn.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM goose_db_version WHERE version_id=2 AND is_applied),(SELECT count(*) FROM suite_versions),to_regprocedure('reject_immutable_history()') IS NULL").Scan(&applied, &versions, &firstFunctionAbsent); err != nil || applied != 0 || versions != 1 || !firstFunctionAbsent {
			t.Fatalf("failed populated upgrade left effects: applied=%d versions=%d first function absent=%v error=%v", applied, versions, firstFunctionAbsent, err)
		}
		schemaExec(t, database.conn, "DROP FUNCTION require_next_authority_revision()")
		if err := migrations.Up(t.Context(), database.url); err != nil {
			t.Fatal(err)
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

func TestPostgresGeneratedCAS(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	schemaExec(t, database.conn, "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','suite',0,'{}')")
	queries := dbgen.New(database.conn)
	arguments := dbgen.CASAuthorityParams{ProjectID: "project", SuiteID: "suite", ExpectedRevision: "0", NewRevision: "1", GovernancePayload: []byte(`{"next":true}`)}
	rows, err := queries.CASAuthority(t.Context(), arguments)
	if err != nil || rows != 1 {
		t.Fatalf("exact nullable-baseline CAS changed %d rows, error %v; want one", rows, err)
	}
	rows, err = queries.CASAuthority(t.Context(), arguments)
	if err != nil || rows != 0 {
		t.Fatalf("stale revision CAS changed %d rows, error %v; want zero", rows, err)
	}
	arguments.ExpectedRevision = "1"
	arguments.NewRevision = "2"
	arguments.ExpectedCurrent = pgtype.Text{String: "wrong", Valid: true}
	rows, err = queries.CASAuthority(t.Context(), arguments)
	if err != nil || rows != 0 {
		t.Fatalf("wrong canonical CAS changed %d rows, error %v; want zero", rows, err)
	}
	var revision, payload string
	if err := database.conn.QueryRow(t.Context(), "SELECT authority_revision::text,governance_payload::text FROM suites WHERE project_id='project' AND suite_id='suite'").Scan(&revision, &payload); err != nil || revision != "1" || payload != `{"next": true}` {
		t.Fatalf("CAS authority result revision=%q payload=%q error=%v", revision, payload, err)
	}
	arguments.ExpectedCurrent = pgtype.Text{}
	arguments.NewRevision = "3"
	rows, err = queries.CASAuthority(t.Context(), arguments)
	if rows != 0 {
		t.Fatalf("failed CAS exposed %d committed rows", rows)
	}
	schemaRequireError(t, err, "23514", "suites_revision_advance")
}

func TestPostgresSchemaDeferredEffects(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	schemaExec(t, database.conn, "INSERT INTO suites(project_id,suite_id,authority_revision,governance_payload) VALUES ('project','suite',0,'{}'),('project','other',0,'{}')")
	for _, test := range []struct {
		name, constraint              string
		promotion, audit, publication bool
	}{
		{"unpromoted version cannot become canonical", "suites_current_promotion_fkey", false, false, false},
		{"promotion requires audit", "promotions_audit_fkey", true, false, true},
		{"promotion requires publication", "promotions_publication_fkey", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := database.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer transaction.Rollback(context.Background())
			schemaExec(t, transaction, "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','suite','staged',$1,'{}')", schemaDigest)
			if test.promotion {
				schemaExec(t, transaction, "INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) VALUES ('staged','project','suite',2,'{}')")
				schemaExec(t, transaction, "INSERT INTO promotions(operation_id,project_id,suite_id,operation_kind,version_id,proposal_id,proposal_revision_id,carrier_id,source_revision,target_id,recorded_at,promotion_payload) VALUES ('staged','project','suite',2,'staged','proposal','revision','carrier','source','target',now(),'{}')")
			}
			if test.audit {
				schemaExec(t, transaction, "INSERT INTO audit_events(operation_id,project_id,suite_id,event_kind,event_payload) VALUES ('staged','project','suite',2,'{}')")
			}
			if test.publication {
				schemaExec(t, transaction, "INSERT INTO publication_intents(operation_id,project_id,suite_id,publication_payload) VALUES ('staged','project','suite','{}')")
			}
			schemaExec(t, transaction, "UPDATE suites SET current_version_id='staged',authority_revision=1 WHERE suite_id='suite'")
			schemaRequireError(t, transaction.Commit(t.Context()), "23503", test.constraint)
			var versions, operations, promotions int
			var revision string
			if err := database.conn.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM suite_versions),(SELECT count(*) FROM operation_receipts),(SELECT count(*) FROM promotions),authority_revision::text FROM suites WHERE suite_id='suite'").Scan(&versions, &operations, &promotions, &revision); err != nil || versions != 0 || operations != 0 || promotions != 0 || revision != "0" {
				t.Fatalf("deferred failure left partial effects: versions=%d operations=%d promotions=%d revision=%q error=%v", versions, operations, promotions, revision, err)
			}
		})
	}
	schemaExec(t, database.conn, "INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) VALUES ('promotion-kind','project','suite',1,'{}')")
	_, err := database.conn.Exec(t.Context(), "INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('wrong-kind','project','suite','promotion-kind')")
	schemaRequireError(t, err, "23503", "consent_sources_operation_fkey")
	_, err = database.conn.Exec(t.Context(), "INSERT INTO audit_events(operation_id,project_id,suite_id,event_kind,event_payload) VALUES ('promotion-kind','project','suite',4,'{}')")
	schemaRequireError(t, err, "23503", "audit_events_operation_fkey")
	_, err = database.conn.Exec(t.Context(), "INSERT INTO consent_acknowledgments(operation_id,project_id,suite_id,acknowledgment_payload) VALUES ('promotion-kind','project','suite','{}')")
	schemaRequireError(t, err, "23503", "consent_acknowledgments_operation_fkey")
	transaction, err := database.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(context.Background())
	schemaExec(t, transaction, "INSERT INTO suite_versions(project_id,suite_id,version_id,manifest_digest,version_payload) VALUES ('project','other','foreign',$1,'{}'),('project','suite','local',$1,'{}')", schemaDigest)
	_, err = transaction.Exec(t.Context(), "INSERT INTO promotions(operation_id,project_id,suite_id,operation_kind,version_id,proposal_id,proposal_revision_id,expected_version_id,carrier_id,source_revision,target_id,recorded_at,promotion_payload) VALUES ('promotion-kind','project','suite',1,'local','proposal','revision','foreign','carrier','source','target',now(),'{}')")
	schemaRequireError(t, err, "23503", "promotions_expected_version_fkey")
}

func TestPostgresGeneratedRoundTrip(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	queries := dbgen.New(database.conn)
	if err := queries.InsertInitialAuthority(t.Context(), dbgen.InsertInitialAuthorityParams{ProjectID: "project", SuiteID: "suite", AuthorityRevision: "7", GovernancePayload: []byte(`{"state":1}`)}); err != nil {
		t.Fatal(err)
	}
	authority, err := queries.GetAuthority(t.Context(), dbgen.GetAuthorityParams{ProjectID: "project", SuiteID: "suite"})
	if err != nil || authority.AuthorityRevision != "7" || authority.CurrentVersionID.Valid {
		t.Fatalf("generated authority round trip: %+v error=%v", authority, err)
	}
	transaction, err := database.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(context.Background())
	queries = queries.WithTx(transaction)
	locked, err := queries.LockAuthority(t.Context(), dbgen.LockAuthorityParams{ProjectID: "project", SuiteID: "suite"})
	if err != nil || locked.AuthorityRevision != "7" {
		t.Fatalf("generated locked authority: %+v error=%v", locked, err)
	}
	if err := queries.InsertVersion(t.Context(), dbgen.InsertVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "version", ManifestDigest: schemaDigest, VersionPayload: []byte(`{"version":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := queries.InsertOperation(t.Context(), dbgen.InsertOperationParams{OperationID: "promotion", ProjectID: "project", SuiteID: "suite", Kind: 2, ReceiptPayload: []byte(`{"promotion":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := queries.InsertOperation(t.Context(), dbgen.InsertOperationParams{OperationID: "consent", ProjectID: "project", SuiteID: "suite", Kind: 4, ReceiptPayload: []byte(`{"consent":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := queries.InsertConsentSource(t.Context(), dbgen.InsertConsentSourceParams{SourceCommandID: "source", ProjectID: "project", SuiteID: "suite", OperationID: "consent"}); err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 10, 2, 12, 0, 0, 123000000, time.UTC)
	if err := queries.InsertPromotion(t.Context(), dbgen.InsertPromotionParams{OperationID: "promotion", ProjectID: "project", SuiteID: "suite", OperationKind: 2, VersionID: "version", ProposalID: "proposal", ProposalRevisionID: "revision", CarrierID: "carrier", SourceRevision: "source", TargetID: "target", RecordedAt: pgtype.Timestamptz{Time: recorded, Valid: true}, PromotionPayload: []byte(`{"record":1}`)}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct {
		id   string
		kind int16
	}{{"promotion", 2}, {"consent", 4}} {
		if err := queries.InsertAudit(t.Context(), dbgen.InsertAuditParams{OperationID: value.id, ProjectID: "project", SuiteID: "suite", EventKind: value.kind, EventPayload: []byte(`{"audit":1}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := queries.InsertPublication(t.Context(), dbgen.InsertPublicationParams{OperationID: "promotion", ProjectID: "project", SuiteID: "suite", PublicationPayload: []byte(`{"publication":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := queries.InsertAcknowledgment(t.Context(), dbgen.InsertAcknowledgmentParams{OperationID: "consent", ProjectID: "project", SuiteID: "suite", AcknowledgmentPayload: []byte(`{"ack":1}`)}); err != nil {
		t.Fatal(err)
	}
	if rows, err := queries.CASAuthority(t.Context(), dbgen.CASAuthorityParams{ProjectID: "project", SuiteID: "suite", ExpectedRevision: "7", NewRevision: "8", NewCurrent: pgtype.Text{String: "version", Valid: true}, GovernancePayload: []byte(`{"state":2}`)}); err != nil || rows != 1 {
		t.Fatalf("generated promotion CAS rows=%d error=%v", rows, err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	queries = dbgen.New(database.conn)
	version, err := queries.GetVersion(t.Context(), dbgen.GetVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "version"})
	if err != nil || version.ManifestDigest != schemaDigest || string(version.VersionPayload) != `{"version": 1}` {
		t.Fatalf("version round trip: %+v error=%v", version, err)
	}
	operation, err := queries.FindOperation(t.Context(), "consent")
	if err != nil || operation.Kind != 4 || operation.ProjectID != "project" {
		t.Fatalf("operation round trip: %+v error=%v", operation, err)
	}
	source, err := queries.FindConsentSource(t.Context(), "source")
	if err != nil || source.OperationID != operation.OperationID || string(source.ReceiptPayload) != string(operation.ReceiptPayload) {
		t.Fatalf("source round trip: %+v error=%v", source, err)
	}
	promotion, err := queries.FindPromotionByReference(t.Context(), dbgen.FindPromotionByReferenceParams{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", ProposalRevisionID: "revision"})
	if err != nil || promotion.VersionID != "version" || !promotion.RecordedAt.Time.Equal(recorded) {
		t.Fatalf("promotion round trip: %+v error=%v", promotion, err)
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
