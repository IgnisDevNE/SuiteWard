package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// hidden is the tail of a password that an unencoded @ would leave in an error of pgx.
const hidden = "S3cr3tPW"

// Like the service URL, an owner URL with an unencoded @ in the password must be refused with a fixed message.
func TestMigrateRefusesAMalformedOwnerURLWithoutEchoingIt(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL":           "postgres://app@127.0.0.1:1/db?sslmode=disable",
		"SUITEWARD_MIGRATION_DATABASE_URL": "postgres://o:p@" + hidden + "@127.0.0.1:1/db?sslmode=bogus",
		"SUITEWARD_ARTIFACT_DIR":           t.TempDir(),
	})
	code, stdout, stderr := run([]string{"migrate"}, env)
	if code != 1 || !strings.Contains(stderr, "SUITEWARD_MIGRATION_DATABASE_URL is not a valid PostgreSQL connection string") {
		t.Fatalf("migrate with a malformed owner URL exited %d, stderr %q; want 1 and the fixed message", code, stderr)
	}
	if strings.Contains(stdout+stderr, hidden) {
		t.Fatalf("migrate printed the password: %q %q", stdout, stderr)
	}
}

// A stop request ends the wait between two attempts instead of sleeping out the interval.
func TestWaitForDatabaseStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := waitForDatabase(ctx, "postgres://owner@127.0.0.1:1/db?sslmode=disable", migrateWait{attempts: 1000, interval: time.Hour, timeout: time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForDatabase = %v; want the context's deadline", err)
	}
}
