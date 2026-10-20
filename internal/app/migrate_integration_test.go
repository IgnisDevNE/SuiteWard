//go:build integration

package app

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/pgtest"
)

// migrateVars is the environment of `suiteward migrate` for the schema of ownerURL: the service connects as the test
// application role, the migration as the owner.
func migrateVars(t *testing.T, ownerURL string) map[string]string {
	t.Helper()
	pgtest.EnsureRole(t, ownerURL)
	vars := serveVars(t, pgtest.AsRole(t, ownerURL))
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = ownerURL
	vars["SUITEWARD_DATABASE_APP_ROLE"] = pgtest.Role
	return vars
}

func TestServeRefusesADatabaseNobodyMigrated(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	pgtest.EnsureRole(t, ownerURL)
	code, stdout, _ := runWithin(t, []string{"serve"}, testEnv(serveVars(t, ownerURL)))
	if code != 1 || !strings.Contains(stdout, "suiteward migrate") {
		t.Fatalf("serve on an empty database exited %d, stdout %q; want 1 and the command that fixes it", code, stdout)
	}
	if n := scalar(t, ownerURL, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()"); n != "0" {
		t.Fatalf("serve created %s tables; it must not migrate", n)
	}
}

func TestServeRefusesAnOlderSchemaAndNamesBothVersions(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	if err := migrations.UpTo(t.Context(), ownerURL, migrations.SupportedVersion-1); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runWithin(t, []string{"serve"}, testEnv(serveVars(t, ownerURL)))
	if want := "schema version " + strconv.FormatInt(migrations.SupportedVersion-1, 10); code != 1 || !strings.Contains(stdout, "suiteward migrate") || !strings.Contains(stdout, want) {
		t.Fatalf("serve on an older schema exited %d, stdout %q; want 1, both versions and the command", code, stdout)
	}
}

func TestSetupRefusesADatabaseNobodyMigrated(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := serveVars(t, ownerURL)
	githubVars(t, vars, newFakeGitHub(t))
	code, _, stderr := runWithin(t, []string{"setup", "--owner", "owner", "--test-check", "acme/widgets=ci / test"}, testEnv(vars))
	if code != 1 || !strings.Contains(stderr, "suiteward migrate") {
		t.Fatalf("setup on an empty database exited %d, stderr %q; want 1 and the command that fixes it", code, stderr)
	}
}

func TestMigrateAppliesTheSchemaAndTheRoleGrantsSoServeRunsAsTheApplicationRole(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := migrateVars(t, ownerURL)
	for range 2 { // the second run changes nothing and still succeeds
		if code, _, stderr := run([]string{"migrate"}, testEnv(vars)); code != 0 {
			t.Fatalf("migrate exited %d: %s", code, stderr)
		}
	}
	if version := scalar(t, ownerURL, "SELECT schema_version()::text"); version != strconv.FormatInt(migrations.SupportedVersion, 10) {
		t.Fatalf("schema version after migrate = %s", version)
	}
	s := startRun(t, vars)
	s.waitReady()
	if code, _ := s.get("/status"); code != http.StatusOK {
		t.Fatalf("GET /status = %d; the service did not run on the application role", code)
	}
	conn, err := pgx.Connect(t.Context(), vars["SUITEWARD_DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }() // a failed close only means the connection is gone
	if _, err := conn.Exec(t.Context(), "ALTER TABLE suite_versions DISABLE TRIGGER USER"); err == nil {
		t.Fatal("the service's role can disable an immutability trigger")
	}
}

func TestMigrateNeedsTheOwnerURL(t *testing.T) {
	vars := serveVars(t, "postgres://app@localhost/db")
	code, _, stderr := run([]string{"migrate"}, testEnv(vars))
	if code != 2 || !strings.Contains(stderr, "SUITEWARD_MIGRATION_DATABASE_URL") {
		t.Fatalf("migrate without the owner URL exited %d, stderr %q; want 2 naming the setting", code, stderr)
	}
}

func TestMigrateNamesAMissingRoleAndLeaksNoSecret(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := migrateVars(t, ownerURL)
	vars["SUITEWARD_DATABASE_APP_ROLE"] = "suiteward_no_such_role"
	code, _, stderr := run([]string{"migrate"}, testEnv(vars))
	if code != 1 || !strings.Contains(stderr, "suiteward_no_such_role") {
		t.Fatalf("migrate with a missing role exited %d, stderr %q; want 1 naming the role", code, stderr)
	}
	if strings.Contains(stderr, password(t, ownerURL)) {
		t.Fatalf("migrate printed the database password: %s", stderr)
	}
}

func TestMigrateWaitsForADatabaseThatIsNotAcceptingConnections(t *testing.T) {
	vars := migrateVars(t, newEmptyDatabase(t))
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = "postgres://owner:pw@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"
	restore := migrateWait(1, 0)
	defer restore()
	if code, _, stderr := run([]string{"migrate"}, testEnv(vars)); code != 1 || !strings.Contains(stderr, "not reachable") {
		t.Fatalf("migrate against a closed port exited %d, stderr %q; want 1 and an unreachable database", code, stderr)
	}
}

// migrateWait bounds how long migrate waits for the database, for one test.
func migrateWait(attempts int, interval time.Duration) (restore func()) {
	previousAttempts, previousInterval := connectAttempts, connectInterval
	connectAttempts, connectInterval = attempts, interval
	return func() { connectAttempts, connectInterval = previousAttempts, previousInterval }
}

// runWithin is run for a command that must end by itself: when it does not (a serve that migrated and started), the
// context ends it after the bound and the assertions that follow report what it did.
func runWithin(t *testing.T, args []string, env Env) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var out, errOut strings.Builder
	env.Stdin = strings.NewReader("")
	code = Run(ctx, args, env, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestMigrateDoesNotWaitOnARefusedLogin(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := migrateVars(t, ownerURL)
	refused, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	refused.User = url.UserPassword(refused.User.Username(), "not-the-password")
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = refused.String()
	defer migrateWait(1000, time.Hour)() // a retry would wait out the interval
	code, _, stderr := runWithin(t, []string{"migrate"}, testEnv(vars))
	if code != 1 || !strings.Contains(stderr, "refused") {
		t.Fatalf("migrate with a wrong password exited %d, stderr %q; want 1 and a refusal", code, stderr)
	}
}

func TestMigrateReachesADatabaseThatStartsAcceptingConnectionsLater(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := migrateVars(t, ownerURL)
	parsed, err := url.Parse(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	_ = free.Close() // only the address is wanted: nothing listens on it yet
	target := parsed.Host
	parsed.Host = addr
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = parsed.String()
	defer migrateWait(100, 50*time.Millisecond)()
	late := time.AfterFunc(300*time.Millisecond, func() { newProxyAt(t, addr, target) })
	defer late.Stop()
	if code, _, stderr := runWithin(t, []string{"migrate"}, testEnv(vars)); code != 0 {
		t.Fatalf("migrate against a database that came up late exited %d: %s", code, stderr)
	}
}
