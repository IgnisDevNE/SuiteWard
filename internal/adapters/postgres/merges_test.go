//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func init() {
	governanceRows["pr_merges"] = map[string]any{"project_id": "project", "suite_id": "suite", "carrier": "github-pr:10/1", "kind": "promoted", "reason": "", "source": "sha-1",
		"version_id": "v1", "corrects_version_id": nil, "proposal_id": "p1", "revision_id": "r1", "recorded_at": "2026-10-15T12:00:00Z"}
	governanceRows["canonical_integrity"] = map[string]any{"project_id": "project", "suite_id": "suite", "version_id": "v1", "verdict": "passed", "checked_at": "2026-10-15T12:00:00Z"}
}

const mergeSHA = "0123456789abcdef0123456789abcdef01234567"

func TestMergeTablesExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 15 {
		t.Fatalf("SupportedVersion = %d; the merge tables are migration 15", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('pr_merges') IS NOT NULL AND to_regclass('canonical_integrity') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("merge tables present = %v, error %v", present, err)
	}
}

// A database at version 14 keeps its observations: the new columns read as not observed and not resolved.
func TestMergeMigrationKeepsEarlierObservations(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 14); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "github_repositories", nil)
	mustInsert(t, database.conn, "github_pulls", nil)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var headRepoNull, unresolved, notMerged, notProcessed, noWatermark bool
	if err := database.conn.QueryRow(t.Context(), `SELECT p.head_repo IS NULL, p.resolved_for IS NULL AND p.resolved_at IS NULL, NOT p.merged AND p.merged_at IS NULL AND p.merge_commit_sha IS NULL,
		NOT p.merge_processed, r.closed_watermark IS NULL FROM github_pulls p JOIN github_repositories r USING (repo)`).Scan(&headRepoNull, &unresolved, &notMerged, &notProcessed, &noWatermark); err != nil {
		t.Fatal(err)
	}
	if !headRepoNull || !unresolved || !notMerged || !notProcessed || !noWatermark {
		t.Fatalf("migrated row: head_repo null %v, unresolved %v, not merged %v, not processed %v, no watermark %v; want all true", headRepoNull, unresolved, notMerged, notProcessed, noWatermark)
	}
}

func TestGitHubPullMergeConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "github_repositories", nil)
	merged := map[string]any{"merged": true, "merged_at": "2026-10-15T11:00:00Z", "resolved_for": "2026-10-15T11:30:00Z", "resolved_at": "2026-10-15T12:00:00Z"}
	with := func(extra map[string]any) map[string]any {
		row := map[string]any{}
		for k, v := range merged {
			row[k] = v
		}
		for k, v := range extra {
			row[k] = v
		}
		return row
	}
	for _, test := range []struct {
		name, constraint string
		row              map[string]any
	}{
		{"merged without merged_at", "github_pulls_merged_at_check", map[string]any{"merged": true}},
		{"merged_at without merged", "github_pulls_merged_at_check", map[string]any{"merged_at": "2026-10-15T11:00:00Z"}},
		{"short merge commit", "github_pulls_merge_commit_check", with(map[string]any{"merge_commit_sha": "abc"})},
		{"uppercase merge commit", "github_pulls_merge_commit_check", with(map[string]any{"merge_commit_sha": "0123456789ABCDEF0123456789abcdef01234567"})},
		{"merge commit of an unmerged pull", "github_pulls_merge_commit_check", map[string]any{"merge_commit_sha": mergeSHA}},
		{"resolved_for without resolved_at", "github_pulls_resolution_check", map[string]any{"resolved_for": "2026-10-15T11:30:00Z"}},
		{"resolved_at without resolved_for", "github_pulls_resolution_check", map[string]any{"resolved_at": "2026-10-15T12:00:00Z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schemaRequireError(t, insertRow(t.Context(), database.conn, "github_pulls", test.row), "23514", test.constraint)
		})
	}
	t.Run("a resolved merge", func(t *testing.T) {
		mustInsert(t, database.conn, "github_pulls", with(map[string]any{"merge_commit_sha": mergeSHA, "head_repo": "fork/widgets"}))
		mustInsert(t, database.conn, "github_pulls", with(map[string]any{"number": 2})) // the bound ran out: no merge commit
	})
}

func TestMergeLedgerConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"unknown kind", "pr_merges", "23514", "pr_merges_kind_check", map[string]any{"kind": "merged"}},
		{"unknown reason", "pr_merges", "23514", "pr_merges_reason_check", map[string]any{"kind": "blocked", "reason": "because"}},
		{"blocked without a reason", "pr_merges", "23514", "pr_merges_reason_blocked_check", map[string]any{"kind": "blocked"}},
		{"a reason without blocked", "pr_merges", "23514", "pr_merges_reason_blocked_check", map[string]any{"reason": "fork-unsupported"}},
		{"corrected without a corrected version", "pr_merges", "23514", "pr_merges_corrects_check", map[string]any{"kind": "corrected"}},
		{"a corrected version without corrected", "pr_merges", "23514", "pr_merges_corrects_check", map[string]any{"corrects_version_id": "v1"}},
		{"a proposal without a revision", "pr_merges", "23514", "pr_merges_reference_check", map[string]any{"revision_id": nil}},
		{"a revision without a proposal", "pr_merges", "23514", "pr_merges_reference_check", map[string]any{"proposal_id": nil}},
		{"a promotion without a version", "pr_merges", "23514", "pr_merges_version_check", map[string]any{"version_id": nil}},
		{"a correction without a version", "pr_merges", "23514", "pr_merges_version_check", map[string]any{"kind": "corrected", "corrects_version_id": "v1", "version_id": nil}},
		{"a no-change without a version", "pr_merges", "23514", "pr_merges_version_check", map[string]any{"kind": "no-change", "version_id": nil}},
		{"a not-target without a version", "pr_merges", "23514", "pr_merges_version_check", map[string]any{"kind": "not-target", "version_id": nil}},
		{"an unknown version", "pr_merges", "23503", "pr_merges_version_fkey", map[string]any{"version_id": "v9"}},
		{"an unknown corrected version", "pr_merges", "23503", "pr_merges_corrects_fkey", map[string]any{"kind": "corrected", "corrects_version_id": "v9"}},
		{"an unknown revision", "pr_merges", "23503", "pr_merges_revision_fkey", map[string]any{"revision_id": "r9"}},
		{"unknown verdict", "canonical_integrity", "23514", "canonical_integrity_verdict_check", map[string]any{"verdict": "ok"}},
		{"integrity of an unknown version", "canonical_integrity", "23503", "canonical_integrity_version_fkey", map[string]any{"version_id": "v9"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schemaRequireError(t, insertRow(t.Context(), database.conn, test.table, test.row), test.code, test.constraint)
		})
	}
	t.Run("every kind and reason is accepted", func(t *testing.T) {
		for i, kind := range []string{"promoted", "no-change", "not-target", "before-canonical"} {
			mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/" + string(rune('a'+i)), "kind": kind})
		}
		mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/corrected", "kind": "corrected", "corrects_version_id": "v1"})
		for i, reason := range []string{"unapproved-change", "invalid-source", "assessment-conflict", "approval-missing", "differs-from-approved", "not-current", "canonical-changed",
			"policy-changed", "integrity-not-passed", "integration-mismatch", "correction-context-reused", "fork-unsupported", "shared-merge-commit", "integration-unreadable", "integrity-failed"} {
			mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/blocked-" + string(rune('a'+i)), "kind": "blocked", "reason": reason, "source": ""})
		}
	})
	t.Run("only a blocked or before-canonical outcome may have no version", func(t *testing.T) {
		mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/no-canonical-a", "kind": "before-canonical", "version_id": nil, "source": ""})
		mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/no-canonical-b", "kind": "blocked", "reason": "differs-from-approved", "version_id": nil})
	})
	t.Run("one outcome per carrier", func(t *testing.T) {
		mustInsert(t, database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/once"})
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_merges", map[string]any{"carrier": "github-pr:10/once"}), "23505", "pr_merges_pkey")
	})
	t.Run("one verdict per Suite, replaced in place", func(t *testing.T) {
		mustInsert(t, database.conn, "canonical_integrity", nil)
		schemaRequireError(t, insertRow(t.Context(), database.conn, "canonical_integrity", nil), "23505", "canonical_integrity_pkey")
		schemaExec(t, database.conn, "UPDATE canonical_integrity SET verdict = 'failed'")
	})
}

func TestMergeOutcomesAreImmutable(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, database.conn)
	mustInsert(t, database.conn, "pr_merges", nil)
	for _, statement := range []string{"UPDATE pr_merges SET source = 'other'", "UPDATE pr_merges SET kind = 'no-change'", "DELETE FROM pr_merges", "TRUNCATE pr_merges"} {
		_, err := database.conn.Exec(t.Context(), statement)
		schemaRequireError(t, err, "23514", "immutable_history")
	}
}
