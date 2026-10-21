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

// A member of the migration role inherits its privileges on the tables, and a role that owns a table can drop the
// triggers on it: neither may be the application role.
func TestGrantAppRefusesAMemberOfTheMigrationRoleAndAnOwnerOfATable(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, database schemaDatabase, role string){
		"suiteward_test_member": func(t *testing.T, database schemaDatabase, role string) {
			var owner string
			if err := database.conn.QueryRow(t.Context(), "SELECT current_user").Scan(&owner); err != nil {
				t.Fatal(err)
			}
			schemaExec(t, database.conn, "GRANT "+pgx.Identifier{owner}.Sanitize()+" TO "+pgx.Identifier{role}.Sanitize())
			t.Cleanup(func() { // roles outlive the schema, and the membership would leak into other tests
				_, _ = database.conn.Exec(context.Background(), "REVOKE "+pgx.Identifier{owner}.Sanitize()+" FROM "+pgx.Identifier{role}.Sanitize())
			})
		},
		"suiteward_test_table_owner": func(t *testing.T, database schemaDatabase, role string) {
			r := pgx.Identifier{role}.Sanitize()
			schemaExec(t, database.conn, "GRANT CREATE ON SCHEMA "+pgx.Identifier{database.schema}.Sanitize()+" TO "+r+"; CREATE TABLE owned (x int); ALTER TABLE owned OWNER TO "+r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			database := newSchemaDatabase(t)
			if err := migrations.Up(t.Context(), database.url); err != nil {
				t.Fatal(err)
			}
			pgtest.EnsureNamedRole(t, database.url, name, "")
			setup(t, database, name)
			if err := migrations.GrantApp(t.Context(), database.url, name); err == nil || !strings.Contains(err.Error(), "least-privilege") {
				t.Fatalf("GrantApp for %s = %v; want a least-privilege refusal", name, err)
			}
		})
	}
}

// A schema whose name needs quoting is still the schema the role is checked against.
func TestGrantAppRefusesATableOwnerInASchemaThatNeedsQuoting(t *testing.T) {
	base := newSchemaDatabase(t)
	schema := "Mixed_" + base.schema
	identifier := pgx.Identifier{schema}.Sanitize()
	schemaExec(t, base.conn, "CREATE SCHEMA "+identifier)
	t.Cleanup(func() { _, _ = base.conn.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") }) // cleanup path: the base schema's own cleanup reports a broken connection
	parsed, err := url.Parse(base.url)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", identifier) // quoted, or the server folds the name to lower case
	parsed.RawQuery = query.Encode()
	mixed := parsed.String()
	if err := migrations.Up(t.Context(), mixed); err != nil {
		t.Fatal(err)
	}
	const name = "suiteward_test_quoted_owner"
	pgtest.EnsureNamedRole(t, mixed, name, "")
	schemaExec(t, base.conn, "GRANT CREATE ON SCHEMA "+identifier+" TO "+name+"; CREATE TABLE "+identifier+".owned (x int); ALTER TABLE "+identifier+".owned OWNER TO "+name)
	if err := migrations.GrantApp(t.Context(), mixed, name); err == nil || !strings.Contains(err.Error(), "least-privilege") {
		t.Fatalf("GrantApp for a table owner in %s = %v; want a least-privilege refusal", schema, err)
	}
}

// The owner of the schema is not always the migration role: a member of it can drop what it owns too.
func TestGrantAppRefusesAMemberOfTheSchemaOwner(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	const schemaOwner, member = "suiteward_test_schema_owner_role", "suiteward_test_schema_owner_member"
	pgtest.EnsureNamedRole(t, database.url, schemaOwner, "")
	pgtest.EnsureNamedRole(t, database.url, member, "")
	schemaExec(t, database.conn, "ALTER SCHEMA "+pgx.Identifier{database.schema}.Sanitize()+" OWNER TO "+schemaOwner+"; GRANT "+schemaOwner+" TO "+member)
	t.Cleanup(func() { // roles outlive the schema, and the membership would leak into other tests
		_, _ = database.conn.Exec(context.Background(), "REVOKE "+schemaOwner+" FROM "+member)
	})
	if err := migrations.GrantApp(t.Context(), database.url, member); err == nil || !strings.Contains(err.Error(), "least-privilege") {
		t.Fatalf("GrantApp for a member of the schema owner = %v; want a least-privilege refusal", err)
	}
}

// GrantApp converges: a privilege the role holds that the contract withholds is taken back.
func TestGrantAppRevokesTruncateTriggerAndReferences(t *testing.T) {
	database, _ := appConnection(t)
	schemaExec(t, database.conn, "GRANT TRUNCATE, TRIGGER, REFERENCES ON ALL TABLES IN SCHEMA "+pgx.Identifier{database.schema}.Sanitize()+" TO "+pgx.Identifier{pgtest.Role}.Sanitize())
	if err := migrations.GrantApp(t.Context(), database.url, pgtest.Role); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := database.conn.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.role_table_grants
		WHERE grantee = $1 AND table_schema = current_schema() AND privilege_type IN ('TRUNCATE', 'TRIGGER', 'REFERENCES')`, pgtest.Role).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d withheld privileges remain after GrantApp, %v", left, err)
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
