//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var ownerEnrolledAt = time.Date(2026, 10, 13, 12, 0, 0, 123456000, time.UTC)

func ownerEnrollment(account int64, secret byte, step int64, repositories ...int64) owner.Enrollment {
	projects := make([]owner.ProjectPolicy, len(repositories))
	for i, id := range repositories {
		projects[i] = owner.ProjectPolicy{RepositoryID: id, Repo: "acme/repo", TestCheck: "ci / test"}
	}
	return owner.Enrollment{Owner: owner.Owner{AccountID: account, Login: "login"}, Secret: bytes.Repeat([]byte{secret}, 20), Projects: projects, Step: step,
		Tier: owner.TierIsolated, EnrolledAt: ownerEnrolledAt}
}

// seedOwnerSuite gives project a Suite governed by revision, the way governance would.
func (w *pgWorld) seedOwnerSuite(project contract.ProjectID, revision contract.PolicyRevisionID) error {
	_, err := w.pool.Exec(w.t.Context(), "INSERT INTO suites (project_id, suite_id, revision, current_version_id, target_id, policy_revision_id) VALUES ($1, 'suite', 0, NULL, 'main', $2)",
		string(project), string(revision))
	return err
}

func ownerCount(w *pgWorld, query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(w.t.Context(), query, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func TestOwnerConformance(t *testing.T) {
	ownertest.RunConformance(t, func(t *testing.T) (owner.Store, ownertest.SeedSuite) {
		w := newPGWorld(t)
		return w.store, w.seedOwnerSuite
	})
}

func TestOwnerTablesExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 12 {
		t.Fatalf("SupportedVersion = %d; the owner tables are migration 12", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('owner_enrollment') IS NOT NULL AND to_regclass('owner_test_checks') IS NOT NULL AND to_regclass('totp_consumed_steps') IS NOT NULL AND to_regclass('github_protection_results') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("owner tables present = %v, error %v", present, err)
	}
}

func TestOwnerHistoryTablesRejectMutation(t *testing.T) {
	w := newPGWorld(t)
	if err := w.store.Enroll(t.Context(), ownerEnrollment(7, 1, 100, 10)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, statement string }{
		{"step update", "UPDATE totp_consumed_steps SET step = step + 1"},
		{"step time update", "UPDATE totp_consumed_steps SET consumed_at = now()"},
		{"step delete", "DELETE FROM totp_consumed_steps"},
		{"step truncate", "TRUNCATE totp_consumed_steps"},
		{"test check update", "UPDATE owner_test_checks SET test_check = 'other'"},
		{"test check delete", "DELETE FROM owner_test_checks"},
		{"test check truncate", "TRUNCATE owner_test_checks"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := w.pool.Exec(t.Context(), test.statement)
			schemaRequireError(t, err, "23514", "immutable_history")
		})
	}
	if ownerCount(w, "SELECT count(*) FROM totp_consumed_steps") != 1 || ownerCount(w, "SELECT count(*) FROM owner_test_checks") != 1 {
		t.Fatal("a rejected mutation changed the rows")
	}
}

func TestOwnerConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{1}, 20)
	enrollment := func(overrides map[string]any) map[string]any {
		row := map[string]any{"generation": 1, "account_id": 7, "login": "login", "secret": secret, "tier": "isolated", "policy_revision_id": "setup-1", "enrolled_at": "2026-10-13T12:00:00Z"}
		for k, v := range overrides {
			row[k] = v
		}
		return row
	}
	check := func(overrides map[string]any) map[string]any {
		row := map[string]any{"repository_id": 10, "project_id": "github-repo:10", "repo": "acme/repo", "test_check": "ci", "policy_revision_id": "setup-1"}
		for k, v := range overrides {
			row[k] = v
		}
		return row
	}
	protection := func(overrides map[string]any) map[string]any {
		row := map[string]any{"repository_id": 10, "result": "enforced", "findings": []string{}, "caveats": []string{}, "checked_at": "2026-10-13T12:00:00Z"}
		for k, v := range overrides {
			row[k] = v
		}
		return row
	}
	mustInsert(t, database.conn, "policies", map[string]any{"project_id": "github-repo:10", "revision_id": "setup-1", "owner_id": "github:7", "owner_kind": "human"})
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"secret of 19 bytes", "owner_enrollment", "23514", "owner_enrollment_secret_check", enrollment(map[string]any{"secret": secret[:19]})},
		{"secret of 21 bytes", "owner_enrollment", "23514", "owner_enrollment_secret_check", enrollment(map[string]any{"secret": append(append([]byte{}, secret...), 1)})},
		{"unknown tier", "owner_enrollment", "23514", "owner_enrollment_tier_check", enrollment(map[string]any{"tier": "cloud"})},
		{"generation zero", "owner_enrollment", "23514", "owner_enrollment_generation_check", enrollment(map[string]any{"generation": 0, "policy_revision_id": "setup-0"})},
		{"account id zero", "owner_enrollment", "23514", "owner_enrollment_account_check", enrollment(map[string]any{"account_id": 0})},
		{"revision of another generation", "owner_enrollment", "23514", "owner_enrollment_revision_check", enrollment(map[string]any{"policy_revision_id": "setup-2"})},
		{"a second owner row", "owner_enrollment", "23514", "owner_enrollment_singleton_check", enrollment(map[string]any{"singleton": false})},
		{"test check of another project", "owner_test_checks", "23514", "owner_test_checks_project_check", check(map[string]any{"project_id": "github-repo:11"})},
		{"test check without a policy", "owner_test_checks", "23503", "owner_test_checks_policy_fkey", check(map[string]any{"policy_revision_id": "setup-9"})},
		{"empty test check", "owner_test_checks", "23514", "owner_test_checks_test_check_check", check(map[string]any{"test_check": " "})},
		{"oversized test check", "owner_test_checks", "23514", "owner_test_checks_test_check_check", check(map[string]any{"test_check": string(bytes.Repeat([]byte{'c'}, 201))})},
		{"protection with an unknown result", "github_protection_results", "23514", "github_protection_results_result_check", protection(map[string]any{"result": "fine"})},
		{"findings with a result other than findings", "github_protection_results", "23514", "github_protection_results_findings_check", protection(map[string]any{"findings": []string{"admin-bypass"}})},
		{"result findings without findings", "github_protection_results", "23514", "github_protection_results_findings_check", protection(map[string]any{"result": "findings"})},
		{"unknown finding code", "github_protection_results", "23514", "github_protection_results_finding_codes_check", protection(map[string]any{"result": "findings", "findings": []string{"other"}})},
		{"unknown caveat code", "github_protection_results", "23514", "github_protection_results_caveat_codes_check", protection(map[string]any{"caveats": []string{"other"}})},
		{"consumed step of account zero", "totp_consumed_steps", "23514", "totp_consumed_steps_account_check", map[string]any{"account_id": 0, "step": 1, "consumed_at": "2026-10-13T12:00:00Z", "source_command_id": "setup:1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schemaRequireError(t, insertRow(t.Context(), database.conn, test.table, test.row), test.code, test.constraint)
		})
	}
	t.Run("one enrollment row, one consumed step once", func(t *testing.T) {
		mustInsert(t, database.conn, "owner_enrollment", enrollment(nil))
		schemaRequireError(t, insertRow(t.Context(), database.conn, "owner_enrollment", enrollment(map[string]any{"generation": 2, "policy_revision_id": "setup-2"})), "23505", "owner_enrollment_pkey")
		step := map[string]any{"account_id": 7, "step": 1, "consumed_at": "2026-10-13T12:00:00Z", "source_command_id": "setup:1"}
		mustInsert(t, database.conn, "totp_consumed_steps", step)
		schemaRequireError(t, insertRow(t.Context(), database.conn, "totp_consumed_steps", step), "23505", "totp_consumed_steps_pkey")
	})
}

func TestOwnerEnrollWritesNothingWhenAStatementFails(t *testing.T) {
	w := newPGWorld(t)
	// Projects are written in repository order: the policy of 10 is inserted, then the one of 20 collides.
	if _, err := w.pool.Exec(t.Context(), "INSERT INTO policies (project_id, revision_id, owner_id, owner_kind) VALUES ('github-repo:20', 'setup-1', 'github:1', 'human')"); err != nil {
		t.Fatal(err)
	}
	err := w.store.Enroll(t.Context(), ownerEnrollment(7, 1, 100, 10, 20))
	if err == nil || errors.Is(err, owner.ErrInvalidState) || errors.Is(err, owner.ErrStepUsed) {
		t.Fatalf("Enroll = %v; want the statement's own failure", err)
	}
	for _, query := range []string{"SELECT count(*) FROM owner_enrollment", "SELECT count(*) FROM owner_test_checks", "SELECT count(*) FROM totp_consumed_steps",
		"SELECT count(*) FROM policies WHERE project_id = 'github-repo:10'"} {
		if n := ownerCount(w, query); n != 0 {
			t.Fatalf("%s = %d after a failed Enroll; want 0", query, n)
		}
	}
	if _, found, err := w.store.Current(t.Context()); err != nil || found {
		t.Fatalf("Current = found %v, error %v; want nothing enrolled", found, err)
	}
}

func TestOwnerEnrollAppendsPolicyRevisions(t *testing.T) {
	w := newPGWorld(t)
	if err := w.store.Enroll(t.Context(), ownerEnrollment(7, 1, 100, 10, 20)); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Enroll(t.Context(), ownerEnrollment(8, 2, 100, 10)); err != nil {
		t.Fatal(err)
	}
	rows, err := w.pool.Query(t.Context(), "SELECT project_id, revision_id, owner_id, owner_kind FROM policies ORDER BY project_id, revision_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][4]string
	for rows.Next() {
		var row [4]string
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	want := [][4]string{
		{"github-repo:10", "setup-1", "github:7", "human"}, {"github-repo:10", "setup-2", "github:8", "human"},
		{"github-repo:20", "setup-1", "github:7", "human"},
	}
	if len(got) != len(want) {
		t.Fatalf("policies = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("policies = %v; want %v", got, want)
		}
	}
	if n := ownerCount(w, "SELECT count(*) FROM owner_test_checks"); n != 3 {
		t.Fatalf("test check rows = %d; the first enrollment's two and the second's one stay", n)
	}
}

func TestOwnerConcurrentEnrollmentsSerialize(t *testing.T) {
	w := newPGWorld(t)
	const callers = 4
	errs := make(chan error, callers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range byte(callers) {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errs <- w.store.Enroll(t.Context(), ownerEnrollment(7, i+1, 100+int64(i), 10))
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent Enroll failed: %v", err)
		}
	}
	state, found, err := w.store.Current(t.Context())
	if err != nil || !found || state.PolicyRevision != owner.RevisionID(callers) {
		t.Fatalf("Current = %+v, found %v, error %v; want generation %d", state, found, err, callers)
	}
	if n := ownerCount(w, "SELECT count(*) FROM policies WHERE project_id = 'github-repo:10'"); n != callers {
		t.Fatalf("policy revisions = %d; want %d", n, callers)
	}
}

// The policies Enroll writes are the ones governance reads: a Suite governed by setup-1 loads a Human owner.
func TestOwnerPolicyLoadsThroughGovernance(t *testing.T) {
	w := newPGWorld(t)
	if err := w.store.Enroll(t.Context(), ownerEnrollment(884695, 1, 100, 10)); err != nil {
		t.Fatal(err)
	}
	project := owner.ProjectID(10)
	if err := w.seedOwnerSuite(project, owner.RevisionID(1)); err != nil {
		t.Fatal(err)
	}
	err := w.store.Do(t.Context(), project, "suite", func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return err
		}
		policy := state.Policy
		if policy.ProjectID() != project || policy.RevisionID() != "setup-1" || policy.OwnerID() != "github:884695" {
			t.Errorf("policy = %s/%s owned by %s; want %s/setup-1 owned by github:884695", policy.ProjectID(), policy.RevisionID(), policy.OwnerID(), project)
		}
		human, err := contract.NewPrincipal("github:884695", contract.Human)
		if err != nil {
			return err
		}
		if !policy.CanApprove(human) {
			t.Error("the enrolled owner cannot approve under its own policy")
		}
		agent, err := contract.NewPrincipal("github:884695", contract.Agent)
		if err != nil {
			return err
		}
		if policy.CanApprove(agent) {
			t.Error("an agent principal can approve under the owner's policy")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
