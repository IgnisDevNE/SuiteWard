//go:build integration

package app

import (
	"os"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/pgtest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func selfCheckConfig(t *testing.T, databaseURL string) config.Config {
	t.Helper()
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// enrollForCheck enrolls the owner of the fake's repository through the real store.
func enrollForCheck(t *testing.T, cfg config.Config) {
	t.Helper()
	pool, store, err := connect(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	err = store.Enroll(t.Context(), owner.Enrollment{
		Owner: owner.Owner{AccountID: setupOwnerID, Login: "octo"}, Secret: make([]byte, 20), Step: 1, Tier: owner.TierIsolated, EnrolledAt: setupNow,
		Projects: enrolledProjects,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfCheckDatabasePassesForAnEnrolledOwnerAndTheApplicationRole(t *testing.T) {
	cfg := selfCheckConfig(t, newDatabase(t))
	enrollForCheck(t, cfg)

	schema, role, enrolled, projects := selfCheckDatabaseResults(t.Context(), cfg)

	for _, r := range []selfCheckResult{schema, role, enrolled} {
		if !r.OK {
			t.Errorf("%+v; want it to pass", r)
		}
	}
	if schema.Name != "database.schema" || role.Name != "database.role" || enrolled.Name != "owner.enrolled" {
		t.Errorf("names = %q, %q, %q", schema.Name, role.Name, enrolled.Name)
	}
	if len(projects) != 1 || projects[0] != enrolledProjects[0] {
		t.Errorf("projects = %+v; want the enrolled %+v", projects, enrolledProjects)
	}
}

func TestSelfCheckDatabaseReportsAnOwnerWhoIsNotEnrolledYet(t *testing.T) {
	cfg := selfCheckConfig(t, newDatabase(t))

	schema, role, enrolled, projects := selfCheckDatabaseResults(t.Context(), cfg)

	if !schema.OK || !role.OK || projects != nil {
		t.Errorf("schema %+v, role %+v, projects %+v; want the first two to pass before setup", schema, role, projects)
	}
	if enrolled.OK || !strings.Contains(enrolled.Detail, "no owner") || !strings.Contains(enrolled.Fix, "suiteward setup") {
		t.Errorf("owner.enrolled = %+v; want a failure that says to run setup", enrolled)
	}
}

func TestSelfCheckDatabaseRefusesASuperuserAndARoleThatCanCreateRolesOrBypassRLS(t *testing.T) {
	appURL := newDatabase(t)
	owners := os.Getenv("SUITEWARD_TEST_DATABASE_URL")
	for name, options := range map[string]struct{ role, options, reason string }{
		"superuser":   {"sw_check_super", "SUPERUSER", "superuser"},
		"create role": {"sw_check_createrole", "CREATEROLE", "create roles"},
		"bypass rls":  {"sw_check_bypassrls", "BYPASSRLS", "row-level security"},
	} {
		t.Run(name, func(t *testing.T) {
			url := pgtest.AsUser(t, appURL, options.role)
			pgtest.EnsureNamedRole(t, owners, options.role, options.options)
			_, role, _, _ := selfCheckDatabaseResults(t.Context(), selfCheckConfig(t, url))

			if role.OK || !strings.Contains(role.Detail, options.reason) || !strings.Contains(role.Detail, options.role) || role.Fix == "" {
				t.Errorf("database.role = %+v; want a failure naming the role and %q", role, options.reason)
			}
		})
	}
}

func TestSelfCheckDatabaseReportsASchemaNobodyMigratedAndStillChecksTheRole(t *testing.T) {
	ownerURL := newEmptyDatabase(t)
	pgtest.EnsureRole(t, ownerURL)
	cfg := selfCheckConfig(t, pgtest.AsRole(t, ownerURL))

	schema, role, enrolled, _ := selfCheckDatabaseResults(t.Context(), cfg)

	if schema.OK || !strings.Contains(schema.Fix, "suiteward migrate") {
		t.Errorf("database.schema = %+v; want a failure that says to run migrate", schema)
	}
	if enrolled.OK || !strings.Contains(enrolled.Detail, "not checked") {
		t.Errorf("owner.enrolled = %+v; want it not checked", enrolled)
	}
	if !role.OK {
		t.Errorf("database.role = %+v; the role is least-privilege whatever the schema", role)
	}
}

func TestSelfCheckDatabaseReportsASchemaNewerThanTheBinary(t *testing.T) {
	appURL := newDatabase(t)
	exec(t, asOwner(t, appURL), "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", migrations.SupportedVersion+1)

	schema, _, _, _ := selfCheckDatabaseResults(t.Context(), selfCheckConfig(t, appURL))

	if schema.OK || !strings.Contains(schema.Detail, "newer") || !strings.Contains(schema.Fix, "release") {
		t.Errorf("database.schema = %+v; want a failure that says the schema is newer and to run that release", schema)
	}
}

func TestSelfCheckDatabaseSaysItCannotReachTheDatabaseWithoutPrintingTheURL(t *testing.T) {
	cfg := selfCheckConfig(t, "postgres://suiteward:"+leakWord+"@127.0.0.1:1/suiteward?sslmode=disable")

	schema, role, enrolled, _ := selfCheckDatabaseResults(t.Context(), cfg)

	for _, r := range []selfCheckResult{schema, role, enrolled} {
		if r.OK || r.Detail == "" || r.Fix == "" {
			t.Errorf("%+v; want a failure with a detail and a fix", r)
		}
		if strings.Contains(r.Detail+r.Fix, leakWord) {
			t.Errorf("%+v holds the password", r)
		}
	}
}

func TestSelfCheckRunOnPostgreSQLPassesWhenEverythingIsRight(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	for name, value := range fake.vars() {
		vars[name] = value
	}
	enrollForCheck(t, selfCheckConfig(t, databaseURL))

	code, stdout, stderr := run([]string{"check"}, testEnv(vars))

	if code != 0 || stderr != "" {
		t.Fatalf("check = %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	for _, name := range []string{"github.app", "github.installation", "github.permissions", "github.repositories", protectionName, "owner.enrolled", "database.schema", "database.role"} {
		if !strings.Contains(stdout, "ok "+name+"\n") {
			t.Errorf("stdout lacks %q:\n%s", "ok "+name, stdout)
		}
	}
	if strings.Contains(stdout, "FAIL") || strings.Contains(stdout, password(t, databaseURL)) {
		t.Errorf("stdout has a failure or the password:\n%s", stdout)
	}
}

// databaseSnapshot is the row counts of the tables check could change if it wrote: the enrollment, its test checks,
// the used TOTP steps, the protection results, the outbox and the jobs.
func databaseSnapshot(t *testing.T, databaseURL string) string {
	t.Helper()
	return scalar(t, databaseURL, `SELECT concat_ws(',',
		(SELECT count(*) FROM owner_enrollment), (SELECT count(*) FROM owner_test_checks), (SELECT count(*) FROM totp_consumed_steps),
		(SELECT count(*) FROM github_protection_results), (SELECT count(*) FROM outbox), (SELECT count(*) FROM river_job))`)
}

func TestSelfCheckChangesNothingInTheDatabase(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	for name, value := range fake.vars() {
		vars[name] = value
	}
	enrollForCheck(t, selfCheckConfig(t, databaseURL))
	before := databaseSnapshot(t, databaseURL)
	if before == "0,0,0,0,0,0" {
		t.Fatalf("snapshot %q has no enrollment: the test would prove nothing", before)
	}

	if code, stdout, stderr := run([]string{"check"}, testEnv(vars)); code != 0 {
		t.Fatalf("check = %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}

	if after := databaseSnapshot(t, databaseURL); after != before {
		t.Errorf("row counts (enrollment, test checks, TOTP steps, protection results, outbox, jobs) = %s after check, %s before; check must change nothing", after, before)
	}
}

// An instance without GitHub passes check: the database checks run for real, GitHub and the enrollment (which setup cannot do
// without GitHub) are warnings.
func TestSelfCheckRunOnPostgreSQLWithoutGitHubPassesAndWarns(t *testing.T) {
	databaseURL := newDatabase(t)

	code, stdout, stderr := run([]string{"check"}, testEnv(serveVars(t, databaseURL)))

	if code != 0 || stderr != "" {
		t.Fatalf("check = %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	for _, line := range []string{"WARN github.app: ", "WARN github.repositories: ", "WARN owner.enrolled: ", "ok database.schema\n", "ok database.role\n"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}
	if strings.Contains(stdout, "FAIL") {
		t.Errorf("stdout has a failure:\n%s", stdout)
	}
}
