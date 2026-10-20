//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

const commandDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func init() {
	governanceRows["pr_commands"] = map[string]any{"repository_id": 10, "comment_id": 100, "pull_number": 1, "author_id": 7, "created_at": "2026-10-14T11:00:00Z",
		"body_digest": commandDigest, "disposition": "not-command", "operation_id": nil, "decided_at": "2026-10-14T12:00:00Z"}
}

func TestCommandsConformance(t *testing.T) {
	commandstest.RunConformance(t, func(t *testing.T) commandstest.Env {
		w := newPGWorld(t)
		return commandstest.Env{Store: w.store, Owner: w.store, Sync: w.store, OutboxKeys: func(ctx context.Context) ([]string, error) {
			rows, err := w.pool.Query(ctx, "SELECT key FROM outbox ORDER BY key COLLATE \"C\"")
			if err != nil {
				return nil, err
			}
			return pgx.CollectRows(rows, pgx.RowTo[string])
		}}
	})
}

func TestCommandTablesExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 13 {
		t.Fatalf("SupportedVersion = %d; the command ledger is migration 13", migrations.SupportedVersion)
	}
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := database.conn.QueryRow(t.Context(), "SELECT to_regclass('pr_commands') IS NOT NULL AND to_regclass('suiteward_instance') IS NOT NULL").Scan(&present); err != nil || !present {
		t.Fatalf("command tables present = %v, error %v", present, err)
	}
}

func TestCommandHistoryRejectsMutation(t *testing.T) {
	w := newPGWorld(t)
	entry := commands.Entry{RepositoryID: 10, CommentID: 100, PullNumber: 1, AuthorID: 7, CreatedAt: fxRecordedAt, BodyDigest: artifact.Hash([]byte("body")),
		Disposition: commands.Malformed, DecidedAt: fxRecordedAt}
	if err := w.store.Record(t.Context(), entry, nil); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, statement string }{
		{"ledger update", "UPDATE pr_commands SET disposition = 'processed'"},
		{"ledger key update", "UPDATE pr_commands SET comment_id = comment_id + 1"},
		{"ledger delete", "DELETE FROM pr_commands"},
		{"ledger truncate", "TRUNCATE pr_commands"},
		{"instance update", "UPDATE suiteward_instance SET instance_id = repeat('0', 32)"},
		{"instance delete", "DELETE FROM suiteward_instance"},
		{"instance truncate", "TRUNCATE suiteward_instance"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := w.mutateHistory(test.statement)
			schemaRequireError(t, err, "23514", "immutable_history")
		})
	}
	if ownerCount(w, "SELECT count(*) FROM pr_commands") != 1 || ownerCount(w, "SELECT count(*) FROM suiteward_instance") != 1 {
		t.Fatal("a rejected mutation changed the rows")
	}
}

func TestCommandConstraints(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, table, code, constraint string
		row                           map[string]any
	}{
		{"unknown disposition", "pr_commands", "23514", "pr_commands_disposition_check", map[string]any{"disposition": "approved"}},
		{"processed without an operation", "pr_commands", "23514", "pr_commands_operation_check", map[string]any{"disposition": "processed"}},
		{"an operation without processed", "pr_commands", "23514", "pr_commands_operation_check", map[string]any{"operation_id": "op"}},
		{"an empty operation", "pr_commands", "23514", "pr_commands_operation_check", map[string]any{"disposition": "processed", "operation_id": " "}},
		{"a blank operation without processed", "pr_commands", "23514", "pr_commands_operation_check", map[string]any{"operation_id": " "}},
		{"uppercase digest", "pr_commands", "23514", "pr_commands_body_digest_check", map[string]any{"body_digest": "sha256:ABC"}},
		{"repository zero", "pr_commands", "23514", "pr_commands_repository_check", map[string]any{"repository_id": 0}},
		{"comment zero", "pr_commands", "23514", "pr_commands_comment_check", map[string]any{"comment_id": 0}},
		{"pull zero", "pr_commands", "23514", "pr_commands_pull_check", map[string]any{"pull_number": 0}},
		{"author zero", "pr_commands", "23514", "pr_commands_author_check", map[string]any{"author_id": 0}},
		{"a second singleton row", "suiteward_instance", "23514", "suiteward_instance_singleton_check", map[string]any{"singleton": false, "instance_id": "00000000000000000000000000000000"}},
		{"a second instance id", "suiteward_instance", "23505", "suiteward_instance_pkey", map[string]any{"instance_id": "00000000000000000000000000000000"}},
		{"a short instance id", "suiteward_instance", "23514", "suiteward_instance_id_check", map[string]any{"instance_id": "abc"}},
		{"a consumed step without a source", "totp_consumed_steps", "23502", "", map[string]any{"account_id": 7, "step": 1, "consumed_at": "2026-10-14T12:00:00Z", "source_command_id": nil}},
		{"a consumed step with an empty source", "totp_consumed_steps", "23514", "totp_consumed_steps_source_check", map[string]any{"account_id": 7, "step": 1, "consumed_at": "2026-10-14T12:00:00Z", "source_command_id": " "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			schemaRequireError(t, insertRow(t.Context(), database.conn, test.table, test.row), test.code, test.constraint)
		})
	}
	t.Run("one ledger row per comment", func(t *testing.T) {
		mustInsert(t, database.conn, "pr_commands", nil)
		schemaRequireError(t, insertRow(t.Context(), database.conn, "pr_commands", nil), "23505", "pr_commands_pkey")
		mustInsert(t, database.conn, "pr_commands", map[string]any{"comment_id": 101, "disposition": "processed", "operation_id": "op"})
	})
}

// A database at version 12 holds consumed steps without a source: the migration gives them the setup source of the
// current enrollment's generation, and the history trigger does not stop it.
func TestCommandMigrationBackfillsConsumedSteps(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 12); err != nil {
		t.Fatal(err)
	}
	mustInsert(t, database.conn, "policies", map[string]any{"project_id": "github-repo:10", "revision_id": "setup-3", "owner_id": "github:7", "owner_kind": "human"})
	schemaExec(t, database.conn, "INSERT INTO owner_enrollment (generation, account_id, login, secret, tier, policy_revision_id, enrolled_at) VALUES (3, 7, 'login', $1, 'isolated', 'setup-3', now())",
		bytes.Repeat([]byte{1}, 20))
	schemaExec(t, database.conn, "INSERT INTO totp_consumed_steps (account_id, step, consumed_at) VALUES (7, 100, now()), (7, 101, now())")
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	rows, err := database.conn.Query(t.Context(), "SELECT step, source_command_id FROM totp_consumed_steps ORDER BY step")
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Step   int64
		Source string
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[step])
	if err != nil || !slices.Equal(got, []step{{100, "setup:3"}, {101, "setup:3"}}) {
		t.Fatalf("consumed steps after the migration = %v, error %v; want both consumed by setup:3", got, err)
	}
	// The history stays immutable afterwards.
	_, err = database.conn.Exec(t.Context(), "UPDATE totp_consumed_steps SET source_command_id = 'x'")
	schemaRequireError(t, err, "23514", "immutable_history")
	_, err = database.conn.Exec(t.Context(), "DELETE FROM totp_consumed_steps")
	schemaRequireError(t, err, "23514", "immutable_history")
}

func TestCommandMigrationFailsWithoutAnEnrollmentToBackfillFrom(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 12); err != nil {
		t.Fatal(err)
	}
	schemaExec(t, database.conn, "INSERT INTO totp_consumed_steps (account_id, step, consumed_at) VALUES (7, 100, now())")
	if err := migrations.Up(t.Context(), database.url); err == nil {
		t.Fatal("the migration invented a source for a step no enrollment explains")
	}
	var version int64
	if err := database.conn.QueryRow(t.Context(), "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil || version != 12 {
		t.Fatalf("schema version after the failed migration = %d, error %v; want 12", version, err)
	}
}

func TestEnrollConsumesItsStepWithTheSetupSource(t *testing.T) {
	w := newPGWorld(t)
	for i := range 2 {
		if err := w.store.Enroll(t.Context(), ownerEnrollment(7, byte(i+1), int64(100+i), 10)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := w.pool.Query(t.Context(), "SELECT source_command_id FROM totp_consumed_steps ORDER BY step")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !slices.Equal(sources, []string{string(owner.SetupSource(1)), string(owner.SetupSource(2))}) {
		t.Fatalf("sources = %v, error %v; want one per generation", sources, err)
	}
}

func TestInstanceSurvivesARestart(t *testing.T) {
	w := newPGWorld(t)
	id, err := w.store.Instance(t.Context())
	if err != nil || len(id) != 32 {
		t.Fatalf("Instance = %q, error %v", id, err)
	}
	restarted, err := postgres.NewStore(w.pool, w.content, testInserter())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(t.Context(), w.db.url); err != nil { // a restart migrates again
		t.Fatal(err)
	}
	if again, err := restarted.Instance(t.Context()); err != nil || again != id {
		t.Fatalf("Instance after a restart = %q, error %v; want %q", again, err, id)
	}
	other := newPGWorld(t)
	if second, err := other.store.Instance(t.Context()); err != nil || second == id {
		t.Fatalf("a second installation has Instance %q, error %v; want its own", second, err)
	}
}

// A failing acknowledgment leaves neither the entry nor a message: the second Record collides on the outbox key
// after its ledger row is inserted.
func TestRecordWithAnAcknowledgmentIsAtomic(t *testing.T) {
	w := newPGWorld(t)
	message := func(key string) *governance.OutboxMessage {
		return &governance.OutboxMessage{Key: key, Kind: "pr_comment", Payload: json.RawMessage(`{}`)}
	}
	entry := func(comment int64) commands.Entry {
		return commands.Entry{RepositoryID: 10, CommentID: comment, PullNumber: 1, AuthorID: 7, CreatedAt: fxRecordedAt, BodyDigest: artifact.Hash([]byte("body")),
			Disposition: commands.Malformed, DecidedAt: fxRecordedAt}
	}
	if err := w.store.Record(t.Context(), entry(1), message("ack")); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Record(t.Context(), entry(2), message("ack")); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("Record = %v; want the outbox key conflict", err)
	}
	if n := ownerCount(w, "SELECT count(*) FROM pr_commands"); n != 1 {
		t.Fatalf("ledger rows = %d after a failed Record; want only the first", n)
	}
	if n := ownerCount(w, "SELECT count(*) FROM outbox WHERE project_id IS NULL AND suite_id IS NULL"); n != 1 {
		t.Fatalf("system outbox rows = %d; want the first acknowledgment only", n)
	}
	// The canceled context writes nothing either.
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := w.store.Record(ctx, entry(3), message("ack-3")); err == nil {
		t.Fatal("Record succeeded on an expired context")
	}
	if n := ownerCount(w, "SELECT count(*) FROM pr_commands"); n != 1 {
		t.Fatalf("ledger rows = %d; want 1", n)
	}
}
