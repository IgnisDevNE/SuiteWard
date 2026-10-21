//go:build integration

package app

import (
	"context"
	"io"
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
// application role, the migration as the owner. The role that gets the grants is the user of DATABASE_URL.
func migrateVars(t *testing.T, ownerURL string) map[string]string {
	t.Helper()
	pgtest.EnsureRole(t, ownerURL)
	vars := serveVars(t, pgtest.AsRole(t, ownerURL))
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = ownerURL
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
	delete(vars, "SUITEWARD_MIGRATION_DATABASE_URL") // serve refuses the owner credential
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
	vars["SUITEWARD_DATABASE_URL"] = pgtest.AsUser(t, ownerURL, "suiteward_no_such_role")
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
	if code, _, stderr := runWithinWait(t, migrateWait{attempts: 1, timeout: 5 * time.Second}, []string{"migrate"}, testEnv(vars)); code != 1 || !strings.Contains(stderr, "not reachable") {
		t.Fatalf("migrate against a closed port exited %d, stderr %q; want 1 and an unreachable database", code, stderr)
	}
}

// runWithin is run for a command that must end by itself: when it does not (a serve that migrated and started), the
// context ends it after the bound and the assertions that follow report what it did.
func runWithin(t *testing.T, args []string, env Env) (code int, stdout, stderr string) {
	t.Helper()
	env.Stdin = strings.NewReader("")
	return runBounded(t, args, env, func(ctx context.Context, out, errOut io.Writer) int { return Run(ctx, args, env, out, errOut) })
}

// runWithinWait is runWithin with the wait of migrate.
func runWithinWait(t *testing.T, wait migrateWait, args []string, env Env) (code int, stdout, stderr string) {
	t.Helper()
	env.Stdin = strings.NewReader("")
	return runBounded(t, args, env, func(ctx context.Context, out, errOut io.Writer) int {
		return runWith(ctx, args, env, out, errOut, wait)
	})
}

func runBounded(t *testing.T, args []string, env Env, command func(ctx context.Context, out, errOut io.Writer) int) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var out, errOut strings.Builder
	return command(ctx, &out, &errOut), out.String(), errOut.String()
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
	// a retry would wait out the interval
	code, _, stderr := runWithinWait(t, migrateWait{attempts: 1000, interval: time.Hour, timeout: 5 * time.Second}, []string{"migrate"}, testEnv(vars))
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
	late := time.AfterFunc(300*time.Millisecond, func() { newProxyAt(t, addr, target) })
	defer late.Stop()
	wait := migrateWait{attempts: 100, interval: 50 * time.Millisecond, timeout: 5 * time.Second}
	if code, _, stderr := runWithinWait(t, wait, []string{"migrate"}, testEnv(vars)); code != 0 {
		t.Fatalf("migrate against a database that came up late exited %d: %s", code, stderr)
	}
}

// A service that connects as the owner, or as a role that is a superuser, bypasses row security or owns the schema,
// defeats the least-privilege split: migrate refuses before it migrates anything, and prints no URL.
func TestMigrateRefusesAServiceRoleThatIsNotLeastPrivilege(t *testing.T) {
	for name, options := range map[string]string{"suiteward_test_super": "SUPERUSER", "suiteward_test_bypass": "BYPASSRLS"} {
		t.Run(options, func(t *testing.T) {
			ownerURL := newEmptyDatabase(t)
			vars := migrateVars(t, ownerURL)
			pgtest.EnsureNamedRole(t, ownerURL, name, options)
			vars["SUITEWARD_DATABASE_URL"] = pgtest.AsUser(t, ownerURL, name)
			code, _, stderr := runWithin(t, []string{"migrate"}, testEnv(vars))
			if code != 1 || !strings.Contains(stderr, "least-privilege") || strings.Contains(stderr, password(t, ownerURL)) {
				t.Fatalf("migrate for a %s role exited %d, stderr %q; want 1, the rule and no secret", options, code, stderr)
			}
			requireNothingMigrated(t, ownerURL)
		})
	}
	t.Run("the owner", func(t *testing.T) {
		ownerURL := newEmptyDatabase(t)
		vars := migrateVars(t, ownerURL)
		vars["SUITEWARD_DATABASE_URL"] = ownerURL
		code, _, stderr := runWithin(t, []string{"migrate"}, testEnv(vars))
		if code != 1 || !strings.Contains(stderr, "least-privilege") || strings.Contains(stderr, password(t, ownerURL)) {
			t.Fatalf("migrate for the owner exited %d, stderr %q; want 1, the rule and no secret", code, stderr)
		}
		requireNothingMigrated(t, ownerURL)
	})
}

// A server that accepts the connection and never answers must not hold an attempt for ever.
func TestMigrateBoundsEveryAttempt(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }() // a failed close only means the port is already free
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() }) // held until the test ends
		}
	}()
	vars := migrateVars(t, newEmptyDatabase(t))
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = "postgres://owner:pw@" + silent.Addr().String() + "/db?sslmode=disable"
	start := time.Now()
	code, _, stderr := runWithinWait(t, migrateWait{attempts: 2, timeout: 200 * time.Millisecond}, []string{"migrate"}, testEnv(vars))
	if code != 1 || !strings.Contains(stderr, "not reachable") || time.Since(start) > 10*time.Second {
		t.Fatalf("migrate against a silent server exited %d after %v, stderr %q; want 1, unreachable, and soon", code, time.Since(start), stderr)
	}
}

func TestServeWithAWrongPasswordIsNotASchemaProblem(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	if err := migrations.Up(t.Context(), ownerURL); err != nil {
		t.Fatal(err)
	}
	appURL := pgtest.AppURL(t, ownerURL)
	wrong, err := url.Parse(appURL)
	if err != nil {
		t.Fatal(err)
	}
	wrong.User = url.UserPassword(wrong.User.Username(), "not-the-password")
	code, stdout, _ := runWithin(t, []string{"serve"}, testEnv(serveVars(t, wrong.String())))
	if code != 1 || strings.Contains(stdout, "suiteward migrate") || strings.Contains(stdout, "not ready") {
		t.Fatalf("serve with a wrong password exited %d, stdout %q; want 1 and no advice to migrate", code, stdout)
	}
}

func TestServeOnASchemaNewerThanTheBuildDoesNotAdviseMigrating(t *testing.T) {
	appURL := newDatabase(t)
	exec(t, asOwner(t, appURL), "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", migrations.SupportedVersion+1)
	code, stdout, _ := runWithin(t, []string{"serve"}, testEnv(serveVars(t, appURL)))
	if code != 1 || strings.Contains(stdout, "run `suiteward migrate`") || !strings.Contains(stdout, "newer release") {
		t.Fatalf("serve on a newer schema exited %d, stdout %q; want 1 and the advice to run the newer release", code, stdout)
	}
}

func requireNothingMigrated(t *testing.T, ownerURL string) {
	t.Helper()
	if n := scalar(t, ownerURL, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()"); n != "0" {
		t.Fatalf("migrate created %s tables before it refused the role", n)
	}
}

// A migration that fails stops migrate before it grants anything: the service role gets no access to a half-migrated schema.
func TestMigrateReportsAMigrationThatFailsAndGrantsNothing(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	vars := migrateVars(t, ownerURL)
	exec(t, ownerURL, "CREATE TABLE suites (x int)") // the first migration creates the table of that name
	code, _, stderr := runWithin(t, []string{"migrate"}, testEnv(vars))
	if code != 1 || !strings.Contains(stderr, "migrate the database") {
		t.Fatalf("migrate over a conflicting table exited %d, stderr %q; want 1 and the migration named", code, stderr)
	}
	if n := scalar(t, ownerURL, "SELECT count(*) FROM information_schema.table_privileges WHERE table_schema = current_schema() AND grantee = $1", pgtest.Role); n != "0" {
		t.Fatalf("migrate granted %s privileges after a failed migration", n)
	}
}
