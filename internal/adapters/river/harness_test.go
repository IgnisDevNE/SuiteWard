//go:build integration

package river_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// world is one migrated schema of its own with a Store over a pool.
type world struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *postgres.Store
}

// newWorld migrates a fresh schema; jobs enqueued in it get maxAttempts attempts.
func newWorld(t *testing.T, maxAttempts int) *world {
	t.Helper()
	connectionURL := os.Getenv("SUITEWARD_TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Fatal("SUITEWARD_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(t.Context(), connectionURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL schema fixture: %v", err)
	}
	schema := "sw_river_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("remove owned schema: %v", err)
		}
		_ = admin.Close(context.Background()) // cleanup path: a failed close only means the connection is gone
	})
	parsed, err := url.Parse(connectionURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	if err := migrations.Up(t.Context(), parsed.String()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	content, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewInserter(maxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewStore(pool, content, inserter)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, pool: pool, store: store}
}

// enqueue writes a job of kind and an outbox message of messageKind with no Suite.
func (w *world) enqueue(key, kind, messageKind string, scheduledAt time.Time) int64 {
	w.t.Helper()
	id, err := w.store.EnqueueSystem(w.t.Context(),
		governance.Job{Kind: kind, Args: []byte(`{}`), ScheduledAt: scheduledAt},
		governance.OutboxMessage{Key: key, Kind: messageKind, Payload: []byte(`{"n":1}`)})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) scalar(query string, args ...any) (value string) {
	w.t.Helper()
	if err := w.pool.QueryRow(w.t.Context(), query, args...).Scan(&value); err != nil {
		w.t.Fatal(err)
	}
	return value
}

// row describes an outbox message as "state attempts last_error".
func (w *world) row(key string) string {
	w.t.Helper()
	return w.scalar("SELECT state || ' ' || attempts || ' ' || coalesce(last_error, '-') FROM outbox WHERE key=$1", key)
}

// dbNow is the database clock, which stamps messages when they are written.
func (w *world) dbNow() time.Time {
	w.t.Helper()
	var now time.Time
	if err := w.pool.QueryRow(w.t.Context(), "SELECT now()").Scan(&now); err != nil {
		w.t.Fatal(err)
	}
	return now.UTC()
}

// clock is a controllable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// logCapture collects what a component logs.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
