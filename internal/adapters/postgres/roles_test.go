//go:build integration

package postgres_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/pgtest"
)

// appConnection connects as the application role to a migrated schema of its own.
func appConnection(t *testing.T) (database schemaDatabase, app *pgx.Conn) {
	t.Helper()
	database = newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	app, err := pgx.Connect(t.Context(), pgtest.AppURL(t, database.url))
	if err != nil {
		t.Fatalf("connect as the application role: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) }) // cleanup path: a failed close only means the connection is gone
	return database, app
}

func TestTheApplicationRoleCannotDefeatTheImmutabilityTriggers(t *testing.T) {
	_, app := appConnection(t)
	for _, statement := range []string{
		"ALTER TABLE suite_versions DISABLE TRIGGER suite_versions_immutable",
		"ALTER TABLE suite_versions DISABLE TRIGGER USER",
		"ALTER TABLE outbox DISABLE TRIGGER ALL",
		"DROP TRIGGER suite_versions_immutable ON suite_versions",
		"TRUNCATE suite_versions",
		"TRUNCATE outbox",
		"TRUNCATE pr_commands, suiteward_instance",
		"SET session_replication_role = replica",
		"CREATE TABLE history_copy (x int)",
		"DROP TABLE pr_checks",
		"ALTER TABLE suites DROP CONSTRAINT suites_pkey",
	} {
		_, err := app.Exec(t.Context(), statement)
		schemaRequireError(t, err, "42501", "")
		if _, err := app.Exec(t.Context(), "SELECT 1"); err != nil { // an aborted statement must not break the session
			t.Fatalf("%q: %v", statement, err)
		}
	}
}

func TestTheApplicationRoleKeepsTheImmutabilityAndHasDataAccess(t *testing.T) {
	_, app := appConnection(t)
	seedGovernance(t, app)
	// The triggers fire for it as for everyone.
	_, err := app.Exec(t.Context(), "DELETE FROM suite_versions")
	schemaRequireError(t, err, "23514", "immutable_history")
	mustInsert(t, app, "outbox", map[string]any{"key": "k"})
	if _, err := app.Exec(t.Context(), "UPDATE outbox SET attempts = attempts + 1 WHERE key = 'k'"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := app.QueryRow(t.Context(), "SELECT nextval('river_job_id_seq')").Scan(&id); err != nil {
		t.Fatalf("a sequence: %v", err)
	}
	var version int64
	if err := app.QueryRow(t.Context(), "SELECT schema_version()").Scan(&version); err != nil || version != migrations.SupportedVersion {
		t.Fatalf("schema_version() = %d, %v; want %d", version, err, migrations.SupportedVersion)
	}
	transaction, err := app.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
	if _, err := transaction.Exec(t.Context(), "LOCK TABLE owner_enrollment IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		t.Fatalf("the enrollment lock: %v", err)
	}
}

func TestTheApplicationRoleCannotRewriteTheSchemaVersion(t *testing.T) {
	_, app := appConnection(t)
	for _, statement := range []string{
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, true)",
		"UPDATE goose_db_version SET is_applied = false",
		"DELETE FROM goose_db_version",
	} {
		_, err := app.Exec(t.Context(), statement)
		schemaRequireError(t, err, "42501", "")
	}
}

func TestGrantAppCoversTablesCreatedLater(t *testing.T) {
	database, app := appConnection(t)
	schemaExec(t, database.conn, "CREATE TABLE later (x int); CREATE SEQUENCE later_seq")
	if _, err := app.Exec(t.Context(), "INSERT INTO later VALUES (1)"); err != nil {
		t.Fatalf("a table the owner created after the grants: %v", err)
	}
	if _, err := app.Exec(t.Context(), "SELECT nextval('later_seq')"); err != nil {
		t.Fatalf("a sequence the owner created after the grants: %v", err)
	}
	if _, err := app.Exec(t.Context(), "TRUNCATE later"); err == nil {
		t.Fatal("the application role can truncate a table created later")
	}
}

func TestGrantAppIsIdempotent(t *testing.T) {
	database, _ := appConnection(t)
	if err := migrations.GrantApp(t.Context(), database.url, pgtest.Role); err != nil {
		t.Fatalf("a second run: %v", err)
	}
}

func TestGrantAppRefusesARoleThatIsMissingOrIsTheOwner(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	if err := migrations.GrantApp(t.Context(), database.url, "suiteward_no_such_role"); err == nil || !strings.Contains(err.Error(), "suiteward_no_such_role") {
		t.Fatalf("a missing role: %v; want an error that names it", err)
	}
	var owner string
	if err := database.conn.QueryRow(t.Context(), "SELECT current_user").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := migrations.GrantApp(t.Context(), database.url, owner); err == nil {
		t.Fatal("the migration role was accepted as the application role")
	}
}

// The service must not connect as a role that could undo the split: GrantApp refuses a superuser, a role that
// bypasses row security and the owner of the schema, and grants nothing to them.
func TestGrantAppRefusesARoleThatIsNotLeastPrivilege(t *testing.T) {
	for name, options := range map[string]string{"suiteward_test_super": "SUPERUSER", "suiteward_test_bypass": "BYPASSRLS", "suiteward_test_schema_owner": ""} {
		t.Run(name, func(t *testing.T) {
			database := newSchemaDatabase(t)
			if err := migrations.Up(t.Context(), database.url); err != nil {
				t.Fatal(err)
			}
			pgtest.EnsureNamedRole(t, database.url, name, options)
			if options == "" {
				schemaExec(t, database.conn, "ALTER SCHEMA "+pgx.Identifier{database.schema}.Sanitize()+" OWNER TO "+pgx.Identifier{name}.Sanitize())
			}
			if err := migrations.GrantApp(t.Context(), database.url, name); err == nil || !strings.Contains(err.Error(), "least-privilege") || strings.Contains(err.Error(), password(t, database.url)) {
				t.Fatalf("GrantApp for %s = %v; want a least-privilege refusal without a secret", name, err)
			}
		})
	}
}

func password(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := parsed.User.Password()
	return secret
}

func TestGrantAppRefusesASchemaNobodyMigrated(t *testing.T) {
	database := newSchemaDatabase(t)
	pgtest.EnsureRole(t, database.url)
	if err := migrations.GrantApp(t.Context(), database.url, pgtest.Role); err == nil || !strings.Contains(err.Error(), "grant the application role") {
		t.Fatalf("GrantApp on an empty schema = %v; want the grant named as the failure", err)
	}
}

func TestRoleChecksRefuseAConnectionWithoutACurrentSchema(t *testing.T) {
	database := newSchemaDatabase(t)
	pgtest.EnsureRole(t, database.url)
	parsed, err := url.Parse(database.url)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", "sw_no_such_schema")
	parsed.RawQuery = query.Encode()
	for name, check := range map[string]func() error{
		"GrantApp":     func() error { return migrations.GrantApp(t.Context(), parsed.String(), pgtest.Role) },
		"CheckAppRole": func() error { return migrations.CheckAppRole(t.Context(), parsed.String(), pgtest.Role) },
	} {
		if err := check(); err == nil || !strings.Contains(err.Error(), "no current schema") {
			t.Errorf("%s without a current schema = %v; want a refusal that says so", name, err)
		}
	}
}

func TestCheckAppRoleReportsARoleNameItCannotLookUp(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.CheckAppRole(t.Context(), database.url, "no\x00role"); err == nil || !strings.Contains(err.Error(), "read the schema and the role") {
		t.Fatalf("CheckAppRole for a name with a NUL byte = %v; want the lookup named as the failure", err)
	}
}
