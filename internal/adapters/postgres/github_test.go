//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
)

func TestGitHubObservationsConformance(t *testing.T) {
	githubsynctest.RunConformance(t, func(t *testing.T) githubsync.Store { return newPGWorld(t).store })
}

func TestGitHubObservationTablesExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 11 {
		t.Fatalf("SupportedVersion = %d; the GitHub observations are migration 11", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('github_repositories') IS NOT NULL AND to_regclass('github_pulls') IS NOT NULL AND to_regclass('github_comment_versions') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("GitHub observation tables present = %v, error %v", present, err)
	}
}

func seedGitHubRows(t *testing.T, database schemaDatabase) {
	t.Helper()
	for _, table := range []string{"github_repositories", "github_pulls", "github_comment_versions"} {
		mustInsert(t, database.conn, table, nil)
	}
}

func TestGitHubCommentVersionsAreAppendOnly(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGitHubRows(t, database)
	for _, test := range []struct {
		name, statement string
		constraint      string
	}{
		{"repo", "UPDATE github_comment_versions SET repo='other/repo'", "github_comment_versions_immutable"},
		{"comment id", "UPDATE github_comment_versions SET comment_id=11", "github_comment_versions_immutable"},
		{"updated at", "UPDATE github_comment_versions SET updated_at=now()", "github_comment_versions_immutable"},
		{"pull number", "UPDATE github_comment_versions SET pull_number=2", "github_comment_versions_immutable"},
		{"author id", "UPDATE github_comment_versions SET author_id=8", "github_comment_versions_immutable"},
		{"author login", "UPDATE github_comment_versions SET author_login='someone'", "github_comment_versions_immutable"},
		{"created at", "UPDATE github_comment_versions SET created_at=now()", "github_comment_versions_immutable"},
		{"body", "UPDATE github_comment_versions SET body='edited'", "github_comment_versions_immutable"},
		{"body digest", "UPDATE github_comment_versions SET body_digest='sha256:0000000000000000000000000000000000000000000000000000000000000000'", "github_comment_versions_immutable"},
		{"first seen", "UPDATE github_comment_versions SET first_seen=now()", "github_comment_versions_immutable"},
		{"delete", "DELETE FROM github_comment_versions", "immutable_history"},
		{"truncate", "TRUNCATE github_comment_versions", "immutable_history"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := database.conn.Exec(t.Context(), test.statement)
			schemaRequireError(t, err, "23514", test.constraint)
		})
	}
	t.Run("not seen since moves freely", func(t *testing.T) {
		schemaExec(t, database.conn, "UPDATE github_comment_versions SET not_seen_since=now()")
		schemaExec(t, database.conn, "UPDATE github_comment_versions SET not_seen_since=NULL")
	})
	t.Run("pulls and repositories stay updatable", func(t *testing.T) {
		schemaExec(t, database.conn, "UPDATE github_pulls SET head_sha='moved', not_open_since=now()")
		schemaExec(t, database.conn, "UPDATE github_repositories SET last_error='transport'")
	})
}

func TestGitHubObservationConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGitHubRows(t, database)
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"blank repository", "github_repositories", "23514", "github_repositories_repo_check", map[string]any{"repo": " "}},
		{"no installation", "github_repositories", "23514", "github_repositories_installation_check", map[string]any{"repo": "a/b", "installation_id": 0}},
		{"unknown failure kind", "github_repositories", "23514", "github_repositories_last_error_check", map[string]any{"repo": "a/b", "last_error": "boom"}},
		{"invalid status", "github_repositories", "23514", "github_repositories_last_status_check", map[string]any{"repo": "a/b", "last_status": 42}},
		{"pull of an unknown repository", "github_pulls", "23503", "github_pulls_repo_fkey", map[string]any{"repo": "a/b"}},
		{"pull number", "github_pulls", "23514", "github_pulls_number_check", map[string]any{"number": 0}},
		{"comment on an unknown pull", "github_comment_versions", "23503", "github_comment_versions_pull_fkey", map[string]any{"comment_id": 11, "pull_number": 2}},
		{"digest of other bytes", "github_comment_versions", "23514", "github_comment_versions_body_digest_check", map[string]any{"comment_id": 11, "body": []byte("other")}},
		{"comment id", "github_comment_versions", "23514", "github_comment_versions_comment_id_check", map[string]any{"comment_id": 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction, err := database.conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
			schemaRequireError(t, insertRow(t.Context(), transaction, test.table, test.row), test.code, test.constraint)
		})
	}
}

// Two bodies at one updated_at are two versions; the same body twice is one.
func TestGitHubCommentVersionKeyIncludesTheBodyDigest(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	seedGitHubRows(t, database)
	other := map[string]any{"body": []byte("other"), "body_digest": "sha256:d9298a10d1b0735837dc4bd85dac641b0f3cef27a47e5d53a54f2f3f5b2fcffa"}
	mustInsert(t, database.conn, "github_comment_versions", other)
	schemaRequireError(t, insertRow(t.Context(), database.conn, "github_comment_versions", other), "23505", "github_comment_versions_pkey")
}

// Observations are not governance facts: they write no governance table, no
// queue and no Suite revision.
func TestGitHubObservationsLeaveGovernanceUntouched(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite, newCandidate("p0", "", w.protected("baseline")))
	tables := append([]string{"outbox", "river_job"}, governanceTables...)
	counts := func() (counts []int) {
		for _, table := range tables {
			counts = append(counts, w.count(table))
		}
		return counts
	}
	revision, before := w.revision(), counts()

	cycle := githubsync.Cycle{Repo: "acme/widgets", InstallationID: 42, At: fxRecordedAt,
		OpenPulls: []githubsync.ObservedPull{{Number: 1, HeadSHA: "a", HeadRef: "f", BaseRef: "main", BaseSHA: "b", UpdatedAt: fxRecordedAt}},
		Comments:  map[int][]githubsync.ObservedComment{1: {{CommentID: 10, AuthorID: 7, AuthorLogin: "reviewer", CreatedAt: fxRecordedAt, UpdatedAt: fxRecordedAt, Body: "approved"}}}}
	if err := w.store.RecordCycle(t.Context(), cycle); err != nil {
		t.Fatal(err)
	}
	if err := w.store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/widgets", InstallationID: 42, At: fxRecordedAt, Kind: githubsync.FailureTransport}); err != nil {
		t.Fatal(err)
	}

	if got := w.revision(); got != revision {
		t.Fatalf("Suite revision %d after observations, want %d", got, revision)
	}
	after := counts()
	for i, table := range tables {
		if after[i] != before[i] {
			t.Fatalf("%s has %d rows after observations, want %d", table, after[i], before[i])
		}
	}
	if w.count("github_pulls") != 1 || w.count("github_comment_versions") != 1 {
		t.Fatal("the observations were not stored")
	}
}

// A cycle of one repository waits for whoever holds the repository's row, so
// two cycles never mark or clear from a stale view.
func TestRecordCycleWaitsForTheRepositoryRow(t *testing.T) {
	for _, record := range []struct {
		name string
		run  func(w *pgWorld) error
	}{
		{"cycle", func(w *pgWorld) error {
			return w.store.RecordCycle(t.Context(), githubsync.Cycle{Repo: "acme/widgets", InstallationID: 42, At: fxRecordedAt.Add(time.Minute)})
		}},
		{"failure", func(w *pgWorld) error {
			return w.store.RecordFailure(t.Context(), githubsync.Failure{Repo: "acme/widgets", InstallationID: 42, At: fxRecordedAt, Kind: githubsync.FailureTransport})
		}},
	} {
		t.Run(record.name, func(t *testing.T) {
			w := newPGWorld(t)
			if err := w.store.RecordCycle(t.Context(), githubsync.Cycle{Repo: "acme/widgets", InstallationID: 42, At: fxRecordedAt}); err != nil {
				t.Fatal(err)
			}
			holder, err := w.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
			if _, err := holder.Exec(t.Context(), "SELECT 1 FROM github_repositories WHERE repo = 'acme/widgets' FOR UPDATE"); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- record.run(w) }()
			waiting := false
			for deadline := time.Now().Add(10 * time.Second); !waiting && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if err := w.db.conn.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%INSERT INTO github_repositories%' AND pid <> pg_backend_pid())").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				t.Fatalf("the write finished (%v) while another transaction held the repository row", err)
			default:
			}
			if !waiting {
				t.Fatal("the write never waited for the repository row")
			}
			if err := holder.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
