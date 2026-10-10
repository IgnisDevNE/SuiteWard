//go:build integration

package postgres_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

func TestCountsGroupsJobsAndOutboxByState(t *testing.T) {
	w := newPGWorld(t)
	counts, err := w.store.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(counts.Jobs) != 0 || len(counts.Outbox) != 0 {
		t.Fatalf("an empty database counts %v and %v; want nothing", counts.Jobs, counts.Outbox)
	}

	// Two probes stay available, one is completed, one discarded; the relay job is not counted.
	jobIDs := map[string]int64{}
	for _, name := range []string{"available-1", "available-2", "completed", "discarded", "relay"} {
		kind := "probe"
		if name == "relay" {
			kind = "outbox_relay"
		}
		id, err := w.store.EnqueueSystem(t.Context(),
			governance.Job{Kind: kind, Args: []byte(`{}`)},
			governance.OutboxMessage{Key: "message-" + name, Kind: "probe", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		jobIDs[name] = id
	}
	for name, state := range map[string]string{"completed": "completed", "discarded": "discarded", "relay": "completed"} {
		if _, err := w.pool.Exec(t.Context(), "UPDATE river_job SET state = $1::river_job_state, finalized_at = now() WHERE id = $2", state, jobIDs[name]); err != nil {
			t.Fatal(err)
		}
	}
	for key, state := range map[string]string{"message-completed": postgres.OutboxDelivered, "message-discarded": postgres.OutboxFailed} {
		if _, err := w.pool.Exec(t.Context(), "UPDATE outbox SET state = $1, finished_at = now() WHERE key = $2", state, key); err != nil {
			t.Fatal(err)
		}
	}

	counts, err = w.store.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int64{"available": 2, "completed": 1, "discarded": 1}; !maps.Equal(counts.Jobs, want) {
		t.Fatalf("job counts = %v; want %v (the outbox_relay job is excluded)", counts.Jobs, want)
	}
	if want := map[string]int64{postgres.OutboxPending: 3, postgres.OutboxDelivered: 1, postgres.OutboxFailed: 1}; !maps.Equal(counts.Outbox, want) {
		t.Fatalf("outbox counts = %v; want %v", counts.Outbox, want)
	}
}

func TestCountsReportsAFailingDatabase(t *testing.T) {
	w := newPGWorld(t)
	w.pool.Close()
	if _, err := w.store.Counts(t.Context()); err == nil {
		t.Fatal("Counts succeeded on a closed pool")
	}
}

func TestSchemaVersionRequiresTheSupportedVersion(t *testing.T) {
	w := newPGWorld(t)
	if version, err := w.store.SchemaVersion(t.Context()); err != nil || version != migrations.SupportedVersion {
		t.Fatalf("SchemaVersion = %d, %v; want %d", version, err, migrations.SupportedVersion)
	}
	if _, err := w.db.conn.Exec(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (11, true)"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.SchemaVersion(t.Context()); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("SchemaVersion on schema version 11 = %v; want ErrSchemaNotReady", err)
	}
	w.pool.Close()
	if _, err := w.store.SchemaVersion(t.Context()); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("SchemaVersion on a closed pool = %v; want ErrSchemaNotReady", err)
	}
}
