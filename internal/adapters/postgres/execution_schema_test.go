//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

// The rows of the execution tables of migration 00021 (docs/contracts/persistence.md, "Execution (M2.1; normative)"). job_id is an
// identity column, so the default row of a dependent table names the first job, which executionSeed inserts.

const execAt = "2026-10-26T12:00:00Z"

func execKey(n int) string   { return fmt.Sprintf("%064x", n) }
func execSHA(n int) string   { return fmt.Sprintf("%040x", n) }
func execLease(n int) string { return fmt.Sprintf("%032x", n) }

func init() {
	governanceRows["execution_jobs"] = map[string]any{"job_key": execKey(1), "repository_id": 10, "pull_number": 1, "plan": "canonical", "profile": "go", "head_sha": execSHA(1),
		"base_sha": nil, "governing_digest": schemaDigest, "governing_source": "V1", "declaration_digest": schemaDigest, "canonical_level": "required", "receipt_level": "off",
		"unapproved_paths": []string{}, "changed_paths": []string{}, "changed_total": 0, "state": "pending", "not_before": execAt, "created_at": execAt}
	governanceRows["execution_results"] = map[string]any{"job_id": 1, "status": "complete", "origin": "executor", "reason": nil, "executor_version": "0.4.0-alpha.3",
		"executor_sha256": execKey(2), "image_digest": schemaDigest, "toolchain": "go1.25.1", "runtime": "podman 5.5.0", "started_at": execAt, "ended_at": execAt,
		"received_at": execAt, "body_digest": schemaDigest, "markers": `[]`, "submodules": []string{}}
	governanceRows["execution_tests"] = map[string]any{"job_id": 1, "package": "pkg", "name": "TestA", "file": "pkg/a_test.go", "kind": "declared", "excluded_reason": nil,
		"head_runs": []string{"pass"}, "base_runs": nil, "state": "pass"}
	governanceRows["pr_receipt_checks"] = map[string]any{"repository_id": 10, "pull_number": 1, "head_sha": execSHA(1), "head_since": execAt, "state": "in_progress",
		"reason": "receipt-running", "reference": "", "job_id": 0, "unblocked": false, "generation": 1, "message_key": "receipt-check:10:1:1", "decided_at": execAt}
	governanceRows["execution_unblocks"] = map[string]any{"repository_id": 10, "comment_id": 100, "job_id": 1, "pull_number": 1, "head_sha": execSHA(1), "decided_at": execAt}
	governanceRows["approval_receipts"] = map[string]any{"repository_id": 10, "comment_id": 100, "head_sha": execSHA(1), "receipt_level": "required", "run_state": "complete",
		"job_id": 1, "summary": "met", "unblocked": false, "decided_at": execAt}
}

// executionSeed commits a command entry and one pending job, which has the id 1 that the default rows of the other tables name.
func executionSeed(t *testing.T, connection *pgx.Conn) {
	t.Helper()
	mustInsert(t, connection, "pr_commands", nil)
	mustInsert(t, connection, "execution_jobs", nil)
	var id int64
	if err := connection.QueryRow(t.Context(), "SELECT job_id FROM execution_jobs").Scan(&id); err != nil || id != 1 {
		t.Fatalf("the first job has id %d, error %v; want 1", id, err)
	}
}

// inRolledBackTransaction runs one case in a transaction that is rolled back, so that the next one starts from the seed.
func inRolledBackTransaction(t *testing.T, database schemaDatabase, run func(tx pgx.Tx)) {
	t.Helper()
	transaction, err := database.conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
	run(transaction)
}

func TestExecutionTablesExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 21 {
		t.Fatalf("SupportedVersion = %d; execution is migration 21", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"execution_jobs", "execution_results", "execution_tests", "execution_unblocks", "pr_receipt_checks", "approval_receipts"} {
		var present bool
		if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&present); err != nil || !present {
			t.Fatalf("%s present = %v, error %v", table, present, err)
		}
	}
	for index, table := range map[string]string{"execution_jobs_key_current_idx": "execution_jobs", "execution_jobs_ready_idx": "execution_jobs", "execution_jobs_head_idx": "execution_jobs"} {
		var present bool
		if err := database.conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 AND indexname = $3)",
			database.schema, table, index).Scan(&present); err != nil || !present {
			t.Fatalf("index %s on %s is missing (error %v)", index, table, err)
		}
	}
}

func TestExecutionJobConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	paths := func(n, size int) []string {
		list := make([]string, n)
		for i := range list {
			list[i] = strings.Repeat("p", size)
		}
		return list
	}
	receipt := map[string]any{"plan": "receipt", "base_sha": execSHA(2), "receipt_level": "advisory", "canonical_level": "off"}
	with := func(base map[string]any, more map[string]any) map[string]any {
		row := map[string]any{}
		for k, v := range base {
			row[k] = v
		}
		for k, v := range more {
			row[k] = v
		}
		return row
	}
	for _, test := range []struct {
		name, code, constraint string
		row                    map[string]any
	}{
		{"short key", "23514", "execution_jobs_key_check", map[string]any{"job_key": "abc"}},
		{"upper-case key", "23514", "execution_jobs_key_check", map[string]any{"job_key": strings.ToUpper(execKey(10))}},
		{"repository zero", "23514", "execution_jobs_repository_check", map[string]any{"repository_id": 0}},
		{"pull zero", "23514", "execution_jobs_pull_check", map[string]any{"pull_number": 0}},
		{"unknown plan", "23514", "execution_jobs_plan_check", map[string]any{"plan": "both"}},
		{"another profile", "23514", "execution_jobs_profile_check", map[string]any{"profile": "python"}},
		{"short head", "23514", "execution_jobs_head_check", map[string]any{"head_sha": "abc"}},
		{"upper-case head", "23514", "execution_jobs_head_check", map[string]any{"head_sha": strings.ToUpper(execSHA(10))}},
		{"malformed base", "23514", "execution_jobs_base_check", with(receipt, map[string]any{"base_sha": "abc"})},
		{"receipt without a base", "23514", "execution_jobs_base_plan_check", with(receipt, map[string]any{"base_sha": nil})},
		{"canonical with a base", "23514", "execution_jobs_base_plan_check", map[string]any{"base_sha": execSHA(2)}},
		{"malformed governing digest", "23514", "execution_jobs_governing_digest_check", map[string]any{"governing_digest": "sha256:ABC"}},
		{"malformed declaration digest", "23514", "execution_jobs_declaration_digest_check", map[string]any{"declaration_digest": "abc"}},
		{"version of zero", "23514", "execution_jobs_governing_source_check", map[string]any{"governing_source": "V0"}},
		{"proposal without a revision", "23514", "execution_jobs_governing_source_check", map[string]any{"governing_source": "P1"}},
		{"unknown canonical level", "23514", "execution_jobs_canonical_level_check", map[string]any{"canonical_level": "strict"}},
		{"unknown receipt level", "23514", "execution_jobs_receipt_level_check", map[string]any{"receipt_level": "strict"}},
		{"canonical job with the canonical level off", "23514", "execution_jobs_plan_level_check", map[string]any{"canonical_level": "off"}},
		{"receipt job with the receipt level off", "23514", "execution_jobs_plan_level_check", with(receipt, map[string]any{"receipt_level": "off"})},
		{"101 unapproved paths", "23514", "execution_jobs_unapproved_paths_check", with(receipt, map[string]any{"unapproved_paths": paths(101, 1)})},
		{"unapproved path of 1025 bytes", "23514", "execution_jobs_unapproved_paths_check", with(receipt, map[string]any{"unapproved_paths": paths(1, 1025)})},
		{"101 changed paths", "23514", "execution_jobs_changed_paths_check", with(receipt, map[string]any{"changed_paths": paths(101, 1)})},
		{"changed path of 1025 bytes", "23514", "execution_jobs_changed_paths_check", with(receipt, map[string]any{"changed_paths": paths(1, 1025)})},
		{"canonical job with unapproved paths", "23514", "execution_jobs_canonical_paths_check", map[string]any{"unapproved_paths": []string{"a_test.go"}}},
		{"canonical job with changed paths", "23514", "execution_jobs_canonical_paths_check", map[string]any{"changed_paths": []string{"a_test.go"}}},
		{"negative changed total", "23514", "execution_jobs_changed_total_check", map[string]any{"changed_total": -1}},
		{"unknown state", "23514", "execution_jobs_state_check", map[string]any{"state": "paused"}},
		{"pending with the reason of no executor", "23514", "execution_jobs_reason_check", map[string]any{"reason": "no-executor"}},
		{"pending with a cancel reason", "23514", "execution_jobs_reason_check", map[string]any{"reason": "closed"}},
		{"negative attempts", "23514", "execution_jobs_attempts_check", map[string]any{"attempts": -1}},
		{"negative failures", "23514", "execution_jobs_failures_check", map[string]any{"failures": -1}},
		{"lease id without an attempt", "23514", "execution_jobs_lease_check", map[string]any{"lease_id": execLease(1)}},
		{"leased time without an attempt", "23514", "execution_jobs_lease_check", map[string]any{"leased_at": execAt}},
		{"lease expiry on a pending job", "23514", "execution_jobs_leased_check", map[string]any{"lease_expires_at": execAt}},
		{"deadline on a pending job", "23514", "execution_jobs_leased_check", map[string]any{"deadline_at": execAt}},
		{"finish time on a pending job", "23514", "execution_jobs_finished_check", map[string]any{"finished_at": execAt}},
		{"unavailable without a finish time", "23514", "execution_jobs_finished_check", map[string]any{"state": "unavailable", "reason": "executor-not-configured"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_jobs", test.row), test.code, test.constraint)
			})
		})
	}
	// The rows the contract allows are accepted: pending, a receipt without a base only when created final, and the three created-final reasons.
	for name, row := range map[string]map[string]any{
		"a canonical job":                        {},
		"a receipt job":                          with(receipt, map[string]any{"job_key": execKey(2), "unapproved_paths": paths(100, 1024), "changed_paths": paths(100, 1024)}),
		"a pending job with the reason it keeps": {"job_key": execKey(3), "reason": "github-unavailable"},
		"a job created for no merge base":        with(receipt, map[string]any{"job_key": execKey(4), "base_sha": nil, "state": "unavailable", "reason": "no-merge-base", "finished_at": execAt}),
		"a job created without an executor":      {"job_key": execKey(5), "state": "unavailable", "reason": "executor-not-configured", "finished_at": execAt},
		"a job created with GitHub unavailable":  with(receipt, map[string]any{"job_key": execKey(6), "base_sha": nil, "state": "unavailable", "reason": "github-unavailable", "finished_at": execAt}),
	} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if err := insertRow(t.Context(), tx, "execution_jobs", row); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}

// TestExecutionJobGuards drives the guard triggers of execution_jobs as the owner, who owns the table and so is the one role that the
// triggers, and not a grant, keep from changing it.
func TestExecutionJobGuards(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	lease := "state = 'leased', attempts = 1, lease_id = '" + execLease(1) + "', leased_at = '" + execAt + "', lease_expires_at = '2026-10-26T12:01:00Z', deadline_at = '2026-10-26T12:10:00Z'"
	requeue := "state = 'pending', failures = 1, reason = 'lease-expired', lease_expires_at = NULL, deadline_at = NULL, not_before = '2026-10-26T12:02:00Z'"
	// Each case starts from a job in the state named, brought there by moves the guard allows.
	toLeased := []string{"UPDATE execution_jobs SET " + lease}
	toRequeued := append(slices.Clone(toLeased), "UPDATE execution_jobs SET "+requeue)
	toComplete := append(slices.Clone(toLeased), "UPDATE execution_jobs SET state = 'complete', reason = NULL, lease_expires_at = NULL, deadline_at = NULL, finished_at = '"+execAt+"'")
	toUnavailable := []string{"UPDATE execution_jobs SET state = 'unavailable', reason = 'no-executor', finished_at = '" + execAt + "'"}
	toCancelled := []string{"UPDATE execution_jobs SET state = 'cancelled', reason = 'closed', finished_at = '" + execAt + "'"}

	for _, test := range []struct {
		name       string
		setup      []string
		statement  string
		constraint string // empty: the statement is allowed
	}{
		{"pending to leased", nil, "UPDATE execution_jobs SET " + lease, ""},
		{"pending to unavailable", nil, toUnavailable[0], ""},
		{"pending to cancelled", nil, toCancelled[0], ""},
		{"pending to pending with a reason, a first failure and a wait", nil,
			"UPDATE execution_jobs SET reason = 'github-unavailable', github_since = '" + execAt + "', not_before = '2026-10-26T12:01:00Z'", ""},
		{"pending to complete", nil, "UPDATE execution_jobs SET state = 'complete', reason = NULL, finished_at = '" + execAt + "'", "execution_jobs_transition"},
		{"pending to pending with an attempt", nil, "UPDATE execution_jobs SET attempts = 1, lease_id = '" + execLease(1) + "', leased_at = '" + execAt + "'", "execution_jobs_transition"},
		{"pending to pending with a failure", nil, "UPDATE execution_jobs SET failures = 1", "execution_jobs_transition"},
		{"leased to leased with a longer lease", toLeased, "UPDATE execution_jobs SET lease_expires_at = '2026-10-26T12:02:00Z'", ""},
		{"leased to pending", toLeased, "UPDATE execution_jobs SET " + requeue, ""},
		{"leased to complete", toLeased, "UPDATE execution_jobs SET state = 'complete', reason = NULL, lease_expires_at = NULL, deadline_at = NULL, finished_at = '" + execAt + "'", ""},
		{"leased to unavailable", toLeased, "UPDATE execution_jobs SET state = 'unavailable', reason = 'timeout', lease_expires_at = NULL, deadline_at = NULL, finished_at = '" + execAt + "'", ""},
		{"leased to cancelled", toLeased, "UPDATE execution_jobs SET state = 'cancelled', reason = 'head-moved', lease_expires_at = NULL, deadline_at = NULL, finished_at = '" + execAt + "'", ""},
		{"leased to leased with another lease id", toLeased, "UPDATE execution_jobs SET lease_id = '" + execLease(2) + "'", "execution_jobs_transition"},
		{"leased to leased with another attempt count", toLeased, "UPDATE execution_jobs SET attempts = 2", "execution_jobs_transition"},
		{"queued again to leased", toRequeued, "UPDATE execution_jobs SET state = 'leased', attempts = 2, lease_id = '" + execLease(2) +
			"', leased_at = '2026-10-26T12:03:00Z', lease_expires_at = '2026-10-26T12:04:00Z', deadline_at = '2026-10-26T12:13:00Z'", ""},
		{"complete to anything", toComplete, "UPDATE execution_jobs SET reason = 'closed'", "execution_jobs_final"},
		{"complete to cancelled", toComplete, "UPDATE execution_jobs SET state = 'cancelled', reason = 'closed'", "execution_jobs_final"},
		{"unavailable to pending", toUnavailable, "UPDATE execution_jobs SET state = 'pending', reason = NULL, finished_at = NULL", "execution_jobs_final"},
		{"unavailable to its own reason", toUnavailable, "UPDATE execution_jobs SET reason = 'executor-not-configured'", "execution_jobs_final"},
		{"cancelled to pending", toCancelled, "UPDATE execution_jobs SET state = 'pending', reason = NULL, finished_at = NULL", "execution_jobs_final"},
		{"cancelled with no change", toCancelled, "UPDATE execution_jobs SET job_key = job_key", "execution_jobs_final"},
		{"pending to leased with a lease id that is not hexadecimal", nil, "UPDATE execution_jobs SET " + strings.Replace(lease, execLease(1), "not-a-lease", 1), "execution_jobs_lease_id_check"},
		{"pending to unavailable for a reason that is no code", nil, "UPDATE execution_jobs SET state = 'unavailable', reason = 'bogus', finished_at = '" + execAt + "'", "execution_jobs_reason_check"},
		{"pending to cancelled for a reason of unavailable", nil, "UPDATE execution_jobs SET state = 'cancelled', reason = 'no-executor', finished_at = '" + execAt + "'", "execution_jobs_reason_check"},
		{"leased to unavailable without a reason", toLeased, "UPDATE execution_jobs SET state = 'unavailable', reason = NULL, lease_expires_at = NULL, deadline_at = NULL, finished_at = '" + execAt + "'",
			"execution_jobs_reason_check"},
		{"a new key", nil, "UPDATE execution_jobs SET job_key = '" + execKey(9) + "'", "execution_jobs_identity"},
		{"a new repository", nil, "UPDATE execution_jobs SET repository_id = 11", "execution_jobs_identity"},
		{"a new pull", nil, "UPDATE execution_jobs SET pull_number = 2", "execution_jobs_identity"},
		{"a new plan", nil, "UPDATE execution_jobs SET plan = 'receipt', base_sha = '" + execSHA(2) + "', receipt_level = 'advisory'", "execution_jobs_identity"},
		{"a new head", nil, "UPDATE execution_jobs SET head_sha = '" + execSHA(9) + "'", "execution_jobs_identity"},
		{"a new governing digest", nil, "UPDATE execution_jobs SET governing_digest = 'sha256:" + strings.Repeat("1", 64) + "'", "execution_jobs_identity"},
		{"a new governing source", nil, "UPDATE execution_jobs SET governing_source = 'V2'", "execution_jobs_identity"},
		{"a new declaration digest", nil, "UPDATE execution_jobs SET declaration_digest = 'sha256:" + strings.Repeat("1", 64) + "'", "execution_jobs_identity"},
		{"a new canonical level", nil, "UPDATE execution_jobs SET canonical_level = 'advisory'", "execution_jobs_identity"},
		{"a new receipt level", nil, "UPDATE execution_jobs SET receipt_level = 'advisory'", "execution_jobs_identity"},
		{"new unapproved paths", nil, "UPDATE execution_jobs SET unapproved_paths = ARRAY['a']", "execution_jobs_identity"},
		{"new changed paths", nil, "UPDATE execution_jobs SET changed_paths = ARRAY['a']", "execution_jobs_identity"},
		{"a new changed total", nil, "UPDATE execution_jobs SET changed_total = 1", "execution_jobs_identity"},
		{"a new creation time", nil, "UPDATE execution_jobs SET created_at = '2026-10-26T13:00:00Z'", "execution_jobs_identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				for _, statement := range test.setup {
					schemaExec(t, tx, statement)
				}
				_, err := tx.Exec(t.Context(), test.statement)
				if test.constraint == "" {
					if err != nil {
						t.Fatalf("%s: %v", test.statement, err)
					}
					return
				}
				schemaRequireError(t, err, "23514", test.constraint)
			})
		})
	}
	// A job is inserted pending, or already unavailable for a reason of the service, and with no attempt.
	for name, row := range map[string]map[string]any{
		"leased":                       {"job_key": execKey(2), "state": "leased", "attempts": 1, "lease_id": execLease(1), "leased_at": execAt, "lease_expires_at": execAt, "deadline_at": execAt},
		"complete":                     {"job_key": execKey(3), "state": "complete", "finished_at": execAt},
		"cancelled":                    {"job_key": execKey(4), "state": "cancelled", "reason": "closed", "finished_at": execAt},
		"unavailable for a lost lease": {"job_key": execKey(5), "state": "unavailable", "reason": "lease-expired", "finished_at": execAt},
		"pending with an attempt":      {"job_key": execKey(6), "attempts": 1, "lease_id": execLease(1), "leased_at": execAt},
	} {
		t.Run("inserted "+name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_jobs", row), "23514", "execution_jobs_initial_state")
			})
		})
	}
	// A job that is final is neither deleted nor truncated, and the other states are not either.
	for _, statement := range []string{"DELETE FROM execution_jobs", "TRUNCATE execution_jobs CASCADE"} {
		_, err := database.conn.Exec(t.Context(), statement)
		schemaRequireError(t, err, "23514", "immutable_history")
	}
}

func TestExecutionJobKeyIsUniqueAmongCurrentJobs(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "execution_jobs", nil)
	schemaRequireError(t, insertRow(t.Context(), database.conn, "execution_jobs", nil), "23505", "execution_jobs_key_current_idx")
	// A job that was ended for a binding that is no longer wanted, or only because no executor was configured or seen, makes way.
	for _, ended := range []string{
		"state = 'cancelled', reason = 'head-moved', finished_at = '" + execAt + "'",
		"state = 'unavailable', reason = 'no-executor', finished_at = '" + execAt + "'",
	} {
		schemaExec(t, database.conn, "UPDATE execution_jobs SET "+ended+" WHERE state = 'pending'")
		mustInsert(t, database.conn, "execution_jobs", nil)
	}
	// A job that was created without an executor is not current either: it does not conflict with the pending job that holds the key.
	mustInsert(t, database.conn, "execution_jobs", map[string]any{"state": "unavailable", "reason": "executor-not-configured", "finished_at": execAt})
	schemaRequireError(t, insertRow(t.Context(), database.conn, "execution_jobs", nil), "23505", "execution_jobs_key_current_idx")
	// The other jobs that are final keep the key.
	for _, reason := range []string{"no-merge-base", "github-unavailable"} {
		key := map[string]any{"job_key": execKey(len(reason)), "plan": "receipt", "receipt_level": "advisory", "canonical_level": "off", "state": "unavailable", "reason": reason, "finished_at": execAt}
		mustInsert(t, database.conn, "execution_jobs", key)
		schemaRequireError(t, insertRow(t.Context(), database.conn, "execution_jobs", key), "23505", "execution_jobs_key_current_idx")
	}
}

func TestExecutionResultConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	service := map[string]any{"status": "unavailable", "origin": "service", "reason": "github-unavailable", "executor_version": nil, "executor_sha256": nil, "image_digest": nil,
		"toolchain": nil, "runtime": nil, "body_digest": nil}
	unavailable := map[string]any{"status": "unavailable", "reason": "executor-error", "image_digest": nil, "toolchain": nil, "runtime": nil}
	with := func(base, more map[string]any) map[string]any {
		row := map[string]any{}
		for k, v := range base {
			row[k] = v
		}
		for k, v := range more {
			row[k] = v
		}
		return row
	}
	for _, test := range []struct {
		name, code, constraint string
		row                    map[string]any
	}{
		{"unknown status", "23514", "execution_results_status_check", map[string]any{"status": "pending"}},
		{"unknown origin", "23514", "execution_results_origin_check", map[string]any{"origin": "operator"}},
		{"complete with a reason", "23514", "execution_results_reason_check", map[string]any{"reason": "executor-error"}},
		{"unavailable without a reason", "23514", "execution_results_reason_check", with(unavailable, map[string]any{"reason": nil})},
		{"a reason of the service for an executor", "23514", "execution_results_reason_check", with(unavailable, map[string]any{"reason": "lease-expired"})},
		{"a reason that is no code", "23514", "execution_results_reason_check", with(unavailable, map[string]any{"reason": "bogus"})},
		{"a service row for another reason", "23514", "execution_results_origin_service_check", with(service, map[string]any{"reason": "executor-error"})},
		{"an executor row without a version", "23514", "execution_results_executor_check", map[string]any{"executor_version": nil}},
		{"an executor row without a binary digest", "23514", "execution_results_executor_check", map[string]any{"executor_sha256": nil}},
		{"an executor row without a body digest", "23514", "execution_results_executor_check", map[string]any{"body_digest": nil}},
		{"a service row with a version", "23514", "execution_results_executor_check", with(service, map[string]any{"executor_version": "1"})},
		{"a service row with a body digest", "23514", "execution_results_executor_check", with(service, map[string]any{"body_digest": schemaDigest})},
		{"a version that starts with a dash", "23514", "execution_results_executor_version_check", map[string]any{"executor_version": "-1"}},
		{"a version of 65 bytes", "23514", "execution_results_executor_version_check", map[string]any{"executor_version": strings.Repeat("a", 65)}},
		{"a binary digest in the wrong form", "23514", "execution_results_executor_sha256_check", map[string]any{"executor_sha256": schemaDigest}},
		{"an image digest in the wrong form", "23514", "execution_results_image_digest_check", map[string]any{"image_digest": "abc"}},
		{"a body digest in the wrong form", "23514", "execution_results_body_digest_check", map[string]any{"body_digest": "abc"}},
		{"complete without an image", "23514", "execution_results_complete_check", map[string]any{"image_digest": nil}},
		{"complete without a toolchain", "23514", "execution_results_complete_check", map[string]any{"toolchain": nil}},
		{"complete without a runtime", "23514", "execution_results_complete_check", map[string]any{"runtime": nil}},
		{"a toolchain of 65 bytes", "23514", "execution_results_toolchain_check", map[string]any{"toolchain": strings.Repeat("a", 65)}},
		{"a runtime of 65 bytes", "23514", "execution_results_runtime_check", map[string]any{"runtime": strings.Repeat("a", 65)}},
		{"a service row with a runtime", "23514", "execution_results_runtime_check", with(service, map[string]any{"runtime": "podman 5.5.0"})},
		{"an end before the start", "23514", "execution_results_times_check", map[string]any{"ended_at": "2026-10-26T11:59:59Z"}},
		{"markers that are not an array", "23514", "execution_results_markers_check", map[string]any{"markers": `{}`}},
		{"201 markers", "23514", "execution_results_markers_check", map[string]any{"markers": "[" + strings.TrimSuffix(strings.Repeat(`{"kind":"env-read","path":"a_test.go","line":1},`, 201), ",") + "]"}},
		{"a marker that is not an object", "23514", "execution_results_markers_check", map[string]any{"markers": `[1]`}},
		{"a marker without a line", "23514", "execution_results_markers_check", map[string]any{"markers": `[{"kind":"env-read","path":"a_test.go"}]`}},
		{"101 submodules", "23514", "execution_results_submodules_check", map[string]any{"submodules": append([]string(nil), make([]string, 101)...)}},
		{"101 unparsed files", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": unparsedFiles(101, 20)}},
		{"an unparsed path of 1025 bytes", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": unparsedFiles(1, 1025)}},
		{"an unparsed path that is not a test file", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"pkg/p.go"}}},
		{"an empty unparsed path", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{""}}},
		{"an absolute unparsed path", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"/pkg/p_test.go"}}},
		{"an unparsed path above the tree", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"../p_test.go"}}},
		{"an unparsed path that climbs out and in", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"pkg/../p_test.go"}}},
		{"an unparsed path with an empty segment", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"pkg//p_test.go"}}},
		{"an unparsed path with a dot segment", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"./p_test.go"}}},
		{"an unparsed path with a trailing slash", "23514", "execution_results_unparsed_check", map[string]any{"unparsed": []string{"pkg/p_test.go/"}}},
		{"a result for an unknown job", "23503", "execution_results_job_fkey", map[string]any{"job_id": 99}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_results", test.row), test.code, test.constraint)
			})
		})
	}
	// The rows of the contract are accepted, and a job has one results row.
	for name, row := range map[string]map[string]any{
		"an executor row that is complete":     {},
		"an executor row that is unavailable":  unavailable,
		"a row of the service":                 service,
		"a marker of the longest kind":         {"markers": `[{"kind":"testing-testing","path":"pkg/a_test.go","line":10000000}]`},
		"a marker of a forged event":           {"markers": `[{"kind":"forged-event","path":"pkg/a_test.go","line":1}]`},
		"changed test files that do not parse": {"unparsed": []string{"pkg/p_test.go", "p_test.go", "a/b/c_test.go"}},
		"the longest list of them":             {"unparsed": unparsedFiles(100, 1024)},
	} {
		inRolledBackTransaction(t, database, func(tx pgx.Tx) {
			if err := insertRow(t.Context(), tx, "execution_results", row); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
	mustInsert(t, database.conn, "execution_results", nil)
	schemaRequireError(t, insertRow(t.Context(), database.conn, "execution_results", nil), "23505", "execution_results_pkey")
}

func TestExecutionTestConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	mustInsert(t, database.conn, "execution_results", nil)
	excluded := map[string]any{"state": "excluded", "excluded_reason": "build-constraint", "head_runs": []string{}}
	receipt := map[string]any{"kind": "added", "base_runs": []string{"fail"}, "state": "detects"}
	with := func(base, more map[string]any) map[string]any {
		row := map[string]any{}
		for k, v := range base {
			row[k] = v
		}
		for k, v := range more {
			row[k] = v
		}
		return row
	}
	for _, test := range []struct {
		name, code, constraint string
		row                    map[string]any
	}{
		{"no package", "23514", "execution_tests_package_check", map[string]any{"package": ""}},
		{"a package of 513 bytes", "23514", "execution_tests_package_check", map[string]any{"package": strings.Repeat("a", 513)}},
		{"no name", "23514", "execution_tests_name_check", map[string]any{"name": ""}},
		{"a name of 201 bytes", "23514", "execution_tests_name_check", map[string]any{"name": strings.Repeat("a", 201)}},
		{"no file", "23514", "execution_tests_file_check", map[string]any{"file": ""}},
		{"a file of 1025 bytes", "23514", "execution_tests_file_check", map[string]any{"file": strings.Repeat("a", 1025)}},
		{"an unknown kind", "23514", "execution_tests_kind_check", map[string]any{"kind": "other"}},
		{"an unknown excluded reason", "23514", "execution_tests_excluded_check", with(excluded, map[string]any{"excluded_reason": "tag"})},
		{"an excluded reason on a test that is not excluded", "23514", "execution_tests_excluded_check", map[string]any{"excluded_reason": "build-constraint"}},
		{"the excluded state without a reason", "23514", "execution_tests_excluded_check", with(excluded, map[string]any{"excluded_reason": nil})},
		{"an unknown observation", "23514", "execution_tests_head_runs_check", map[string]any{"head_runs": []string{"ok"}}},
		{"excluded as an observation", "23514", "execution_tests_head_runs_check", map[string]any{"head_runs": []string{"excluded"}}},
		{"no head runs on a test that ran", "23514", "execution_tests_head_runs_check", map[string]any{"head_runs": []string{}}},
		{"four head runs", "23514", "execution_tests_head_runs_check", map[string]any{"head_runs": []string{"pass", "pass", "pass", "pass"}}},
		{"head runs on an excluded test", "23514", "execution_tests_head_runs_check", with(excluded, map[string]any{"head_runs": []string{"pass"}})},
		{"base runs on an excluded test", "23514", "execution_tests_base_runs_check", with(excluded, map[string]any{"base_runs": []string{"pass"}})},
		{"an empty list of base runs", "23514", "execution_tests_base_runs_check", map[string]any{"base_runs": []string{}}},
		{"four base runs", "23514", "execution_tests_base_runs_check", map[string]any{"base_runs": []string{"pass", "pass", "pass", "pass"}}},
		{"an unknown base observation", "23514", "execution_tests_base_runs_check", map[string]any{"base_runs": []string{"ok"}}},
		{"an unknown state", "23514", "execution_tests_state_check", map[string]any{"state": "green"}},
		{"a test of an unknown result", "23503", "execution_tests_result_fkey", map[string]any{"job_id": 99}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "execution_tests", test.row), test.code, test.constraint)
			})
		})
	}
	// Every state of both plans is accepted: fail and fails are two states.
	for i, state := range []string{"pass", "fail", "skipped", "missing", "flaky", "fails", "new_api", "detects", "inert"} {
		mustInsert(t, database.conn, "execution_tests", map[string]any{"name": fmt.Sprintf("Test%d", i), "state": state})
	}
	mustInsert(t, database.conn, "execution_tests", map[string]any{"name": "TestExcluded", "state": "excluded", "excluded_reason": "build-constraint", "head_runs": []string{}})
	mustInsert(t, database.conn, "execution_tests", map[string]any{"name": "TestNested", "state": "excluded", "excluded_reason": "nested-module", "head_runs": []string{}}) // a test under a nested go.mod
	mustInsert(t, database.conn, "execution_tests", with(receipt, map[string]any{"name": "TestReceipt", "head_runs": []string{"pass", "fail", "build-error"}, "base_runs": []string{"skip", "absent"}}))
	schemaRequireError(t, insertRow(t.Context(), database.conn, "execution_tests", map[string]any{"name": "Test0"}), "23505", "execution_tests_pkey")
}

func TestExecutionLedgerConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	executionSeed(t, database.conn)
	none := map[string]any{"run_state": "none", "job_id": nil, "summary": nil}
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"receipt check repository zero", "pr_receipt_checks", "23514", "pr_receipt_checks_repository_check", map[string]any{"repository_id": 0}},
		{"receipt check pull zero", "pr_receipt_checks", "23514", "pr_receipt_checks_pull_check", map[string]any{"pull_number": 0}},
		{"receipt check short head", "pr_receipt_checks", "23514", "pr_receipt_checks_head_check", map[string]any{"head_sha": "abc"}},
		{"receipt check blank reason", "pr_receipt_checks", "23514", "pr_receipt_checks_reason_check", map[string]any{"reason": " "}},
		{"receipt check state", "pr_receipt_checks", "23514", "pr_receipt_checks_state_check", map[string]any{"state": "skipped"}},
		{"receipt check reference", "pr_receipt_checks", "23514", "pr_receipt_checks_reference_check", map[string]any{"reference": "P1"}},
		{"receipt check negative job", "pr_receipt_checks", "23514", "pr_receipt_checks_job_check", map[string]any{"job_id": -1}},
		{"receipt check generation zero", "pr_receipt_checks", "23514", "pr_receipt_checks_generation_check", map[string]any{"generation": 0}},
		{"receipt check blank message key", "pr_receipt_checks", "23514", "pr_receipt_checks_message_key_check", map[string]any{"message_key": " "}},
		{"unblock for an unknown command", "execution_unblocks", "23503", "execution_unblocks_command_fkey", map[string]any{"comment_id": 999}},
		{"unblock for an unknown job", "execution_unblocks", "23503", "execution_unblocks_job_fkey", map[string]any{"job_id": 99}},
		{"unblock pull zero", "execution_unblocks", "23514", "execution_unblocks_pull_check", map[string]any{"pull_number": 0}},
		{"unblock short head", "execution_unblocks", "23514", "execution_unblocks_head_check", map[string]any{"head_sha": "abc"}},
		{"approval for an unknown command", "approval_receipts", "23503", "approval_receipts_command_fkey", map[string]any{"comment_id": 999}},
		{"approval for an unknown job", "approval_receipts", "23503", "approval_receipts_job_fkey", map[string]any{"job_id": 99}},
		{"approval short head", "approval_receipts", "23514", "approval_receipts_head_check", map[string]any{"head_sha": "abc"}},
		{"approval level off", "approval_receipts", "23514", "approval_receipts_level_check", map[string]any{"receipt_level": "off"}},
		{"approval unknown run state", "approval_receipts", "23514", "approval_receipts_run_state_check", map[string]any{"run_state": "running", "job_id": nil, "summary": nil}},
		{"approval none with a job", "approval_receipts", "23514", "approval_receipts_job_check", map[string]any{"run_state": "none"}},
		{"approval not-applicable with a job", "approval_receipts", "23514", "approval_receipts_job_check", map[string]any{"run_state": "not-applicable"}},
		{"approval pending without a job", "approval_receipts", "23514", "approval_receipts_job_check", map[string]any{"run_state": "pending", "job_id": nil, "summary": nil}},
		{"approval complete without a job", "approval_receipts", "23514", "approval_receipts_job_check", map[string]any{"job_id": nil}},
		{"approval unavailable without a job", "approval_receipts", "23514", "approval_receipts_job_check", map[string]any{"run_state": "unavailable", "job_id": nil, "summary": nil}},
		{"approval unknown summary", "approval_receipts", "23514", "approval_receipts_summary_check", map[string]any{"summary": "maybe"}},
		{"approval summary on a pending run", "approval_receipts", "23514", "approval_receipts_summary_check", map[string]any{"run_state": "pending"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, test.table, test.row), test.code, test.constraint)
			})
		})
	}
	// The accepted rows, and one row for each key.
	for table, rows := range map[string][]map[string]any{
		"pr_receipt_checks":  {{"state": "neutral", "reason": "receipt-not-run", "job_id": 5, "unblocked": true}},
		"execution_unblocks": {{}},
		"approval_receipts":  {{}, none},
	} {
		for _, row := range rows {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				if err := insertRow(t.Context(), tx, table, row); err != nil {
					t.Errorf("%s: %v", table, err)
				}
			})
		}
	}
	for _, table := range []string{"pr_receipt_checks", "execution_unblocks", "approval_receipts"} {
		mustInsert(t, database.conn, table, nil)
		schemaRequireError(t, insertRow(t.Context(), database.conn, table, nil), "23505", table+"_pkey")
	}
}

func TestExecutionHistoryRejectsMutation(t *testing.T) {
	w := newPGWorld(t)
	executionSeed(t, w.db.conn)
	for _, table := range []string{"execution_results", "execution_tests", "execution_unblocks", "approval_receipts", "pr_receipt_checks"} {
		mustInsert(t, w.db.conn, table, nil)
	}
	for _, table := range []string{"execution_results", "execution_tests", "execution_unblocks", "approval_receipts"} {
		for _, statement := range []string{"UPDATE " + table + " SET job_id = job_id", "DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			t.Run(statement, func(t *testing.T) {
				schemaRequireError(t, w.mutateHistory(statement), "23514", "immutable_history")
			})
		}
	}
	// The Receipt check is updated in place and never deleted.
	if _, err := w.pool.Exec(t.Context(), "UPDATE pr_receipt_checks SET state = 'success', reason = 'receipt-complete'"); err != nil {
		t.Fatalf("an update in place failed: %v", err)
	}
	for _, statement := range []string{"DELETE FROM pr_receipt_checks", "TRUNCATE pr_receipt_checks"} {
		schemaRequireError(t, w.mutateHistory(statement), "23514", "immutable_history")
	}
	for table, want := range map[string]int{"execution_results": 1, "execution_tests": 1, "execution_unblocks": 1, "approval_receipts": 1, "pr_receipt_checks": 1, "execution_jobs": 1} {
		if got := ownerCount(w, "SELECT count(*) FROM "+table); got != want {
			t.Fatalf("%s has %d rows after the rejected statements; want %d", table, got, want)
		}
	}
}

func TestCommandLedgerAcceptsAModeAndTheExecutionDispositions(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for i, disposition := range []string{"receipt-refused", "mode-refused", "unblocked", "unblock-refused"} {
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 200 + i, "disposition": disposition})
	}
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 210, "disposition": "mode-set", "mode": "refactor"})
	for i, mode := range []string{"fix", "feature", "refactor"} { // an approve of any disposition may carry a mode
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 220 + i, "disposition": "invalid-code", "mode": mode})
	}
	for _, test := range []struct {
		name, constraint string
		row              map[string]any
	}{
		{"an unknown mode", "pr_commands_mode_check", map[string]any{"mode": "bugfix"}},
		{"a mode in capitals", "pr_commands_mode_check", map[string]any{"mode": "Fix"}},
		{"a mode-set without a mode", "pr_commands_mode_set_check", map[string]any{"disposition": "mode-set"}},
		{"an operation on a mode-set", "pr_commands_operation_check", map[string]any{"disposition": "mode-set", "mode": "fix", "operation_id": "op"}},
		{"an operation on a receipt refusal", "pr_commands_operation_check", map[string]any{"disposition": "receipt-refused", "operation_id": "op"}},
		{"an operation on an unblock", "pr_commands_operation_check", map[string]any{"disposition": "unblocked", "operation_id": "op"}},
		{"an unknown disposition", "pr_commands_disposition_check", map[string]any{"disposition": "unblock-approved"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inRolledBackTransaction(t, database, func(tx pgx.Tx) {
				schemaRequireError(t, insertRow(t.Context(), tx, "pr_commands", test.row), "23514", test.constraint)
			})
		})
	}
	// The dispositions of the earlier migrations are still accepted.
	for i, disposition := range []string{"not-command", "before-enrollment", "no-suite", "edited-first", "failed", "ignored-author", "malformed", "invalid-code", "replayed-code",
		"unknown-proposal", "proposed", "propose-refused", "skipped", "skip-refused", "suggested", "suggest-refused", "other-pull"} {
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 300 + i, "disposition": disposition})
	}
	mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 400, "disposition": "processed", "operation_id": "op"})
}

// unparsedFiles are n paths of size bytes each that end _test.go and differ.
func unparsedFiles(n, size int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("%0*d", size-len("_test.go"), i) + "_test.go"
	}
	return paths
}
