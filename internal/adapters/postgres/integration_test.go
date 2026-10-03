//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func composedStore(t *testing.T) (*pgxpool.Pool, *postgres.Store, *filesystem.Store, string) {
	t.Helper()
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	root := t.TempDir()
	objects, err := filesystem.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewStore(pool, objects)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, objects, root
}

func assertNoPromotion(t *testing.T, pool *pgxpool.Pool, store *postgres.Store, fixture governanceFixture) {
	t.Helper()
	snapshot, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil || snapshot.Canonical.Version().ID() != "" || snapshot.Fence.Revision != 1 {
		t.Fatalf("failed promotion changed canonical authority: %+v, %v", snapshot, err)
	}
	for _, table := range []string{"suite_versions", "promotions", "publication_intents"} {
		if got := pgCount(t, pool, table); got != 0 {
			t.Fatalf("failed promotion left %s effects: %d", table, got)
		}
	}
	if pgCount(t, pool, "operation_receipts") != 1 || pgCount(t, pool, "audit_events") != 1 {
		t.Fatal("failed promotion changed the previously committed consent receipt/audit")
	}
}

func TestComposedArtifactsBindCanonicalAuthorityAndReplay(t *testing.T) {
	pool, store, objects, root := composedStore(t)
	fixture := pgGovernanceFixture(t)
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command}); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.Promote(t.Context(), store, fixture.request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing raw artifact allowed promotion: %v", err)
	}
	assertNoPromotion(t, pool, store, fixture)
	digest := artifact.Hash([]byte("protected"))
	if err := objects.Put(t.Context(), digest, bytes.NewBufferString("protected")); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.Promote(t.Context(), store, fixture.request); err != nil {
		t.Fatalf("verified raw artifacts did not allow atomic promotion: %v", err)
	}
	// The manifest digest identifies canonical metadata, not a required raw blob.
	if err := objects.Verify(t.Context(), fixture.request.Proposed.Manifest().Digest()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly stored a manifest metadata blob: %v", err)
	}
	object := filepath.Join(root, "sha256-"+strings.TrimPrefix(digest.String(), "sha256:"))
	if err := os.WriteFile(object, []byte("corrupt local volume"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []governance.ReadRequest{{Reference: fixture.request.Reference}, {Reference: fixture.request.Reference, OperationID: fixture.request.OperationID}} {
		if _, err := store.Load(t.Context(), read); !errors.Is(err, filesystem.ErrCorruptArtifact) {
			t.Fatalf("corrupt referenced artifact allowed current/history replay: %v", err)
		}
	}
	if err := objects.Put(t.Context(), digest, bytes.NewBufferString("protected")); !errors.Is(err, filesystem.ErrCorruptArtifact) {
		t.Fatalf("candidate Put repaired corrupt canonical content: %v", err)
	}
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.Promote(t.Context(), store, fixture.request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing historical artifact allowed exact replay: %v", err)
	}
	if pgCount(t, pool, "promotions") != 1 || pgCount(t, pool, "suite_versions") != 1 {
		t.Fatal("storage corruption rewrote immutable promotion history")
	}
}

func TestComposedPublicationSurvivesDatabaseRollbackWithoutAuthority(t *testing.T) {
	pool, store, objects, _ := composedStore(t)
	fixture := pgGovernanceFixture(t)
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command}); err != nil {
		t.Fatal(err)
	}
	digest := artifact.Hash([]byte("protected"))
	if err := objects.Put(t.Context(), digest, bytes.NewBufferString("protected")); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(t.Context(), `CREATE FUNCTION fail_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture publication failure'; END; $$;
CREATE TRIGGER fixture_fail_publication BEFORE INSERT ON publication_intents FOR EACH ROW EXECUTE FUNCTION fail_publication();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := governance.Promote(t.Context(), store, fixture.request); err == nil {
		t.Fatal("failed database publication intent allowed promotion success")
	}
	assertNoPromotion(t, pool, store, fixture)
	if err := objects.Verify(t.Context(), digest); err != nil {
		t.Fatalf("database rollback damaged independently published immutable content: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER fixture_fail_publication ON publication_intents`); err != nil {
		t.Fatal(err)
	}
	if result, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !result.Committed || result.Duplicate {
		t.Fatalf("safe retry did not create the first durable promotion: %+v, %v", result, err)
	}
}

type promotionReadBarrier struct {
	governance.Store
	ready   chan struct{}
	release chan struct{}
}

func (b promotionReadBarrier) Load(ctx context.Context, request governance.ReadRequest) (governance.Snapshot, error) {
	snapshot, err := b.Store.Load(ctx, request)
	if err == nil {
		b.ready <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return governance.Snapshot{}, ctx.Err()
		}
	}
	return snapshot, err
}

func TestComposedConcurrentPromotionHasOneCanonicalWinner(t *testing.T) {
	pool, store, objects, _ := composedStore(t)
	fixture := pgGovernanceFixture(t)
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatal(err)
	}
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command}); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(t.Context(), artifact.Hash([]byte("protected")), bytes.NewBufferString("protected")); err != nil {
		t.Fatal(err)
	}
	barrier := promotionReadBarrier{Store: store, ready: make(chan struct{}, 2), release: make(chan struct{})}
	requests := []governance.PromoteRequest{fixture.request, fixture.request}
	requests[1].OperationID, requests[1].NewVersionID = "other-promotion", "other-version"
	results := make([]error, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i := range requests {
		workers.Go(func() { _, results[i] = governance.Promote(ctx, barrier, requests[i]) })
	}
	for range requests {
		select {
		case <-barrier.ready:
		case <-ctx.Done():
			close(barrier.release)
			workers.Wait()
			t.Fatalf("both transactions did not reach the coherent read boundary: %v", results)
		}
	}
	close(barrier.release)
	workers.Wait()
	winner := -1
	for i, err := range results {
		if err == nil {
			if winner != -1 {
				t.Fatal("both competing promotions committed")
			}
			winner = i
		} else if !errors.Is(err, governance.ErrAuthorityConflict) {
			t.Fatalf("losing promotion error: %v", err)
		}
	}
	if winner == -1 {
		t.Fatal("neither valid competing promotion committed")
	}
	snapshot, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil || snapshot.Canonical.Version().ID() != requests[winner].NewVersionID || snapshot.Fence.Revision != 2 {
		t.Fatalf("canonical authority is not the exact winning effect: %+v, %v", snapshot, err)
	}
	for _, table := range []string{"suite_versions", "promotions", "publication_intents"} {
		if pgCount(t, pool, table) != 1 {
			t.Fatalf("concurrent promotion duplicated %s", table)
		}
	}
	if pgCount(t, pool, "operation_receipts") != 2 || pgCount(t, pool, "audit_events") != 2 {
		t.Fatal("losing promotion left a receipt or audit effect")
	}
}
