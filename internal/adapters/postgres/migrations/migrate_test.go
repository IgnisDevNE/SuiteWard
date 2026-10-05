package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRejectUnsupportedMigrationTarget(t *testing.T) {
	if err := UpTo(t.Context(), "unused", 0); !errors.Is(err, ErrForwardOnly) {
		t.Fatalf("non-forward target must be rejected: %v", err)
	}
	if err := UpTo(t.Context(), "unused", SupportedVersion+1); err == nil || !strings.Contains(err.Error(), "unsupported schema migration version") {
		t.Fatalf("unknown target must be rejected: %v", err)
	}
}

func TestRejectMalformedMigrationConfiguration(t *testing.T) {
	if err := Up(t.Context(), "postgres://%"); err == nil || !strings.Contains(err.Error(), "parse migration database configuration") {
		t.Fatalf("malformed database configuration must be rejected: %v", err)
	}
}

func TestMigrationHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Up(ctx, "postgres://nobody@127.0.0.1:1/test?sslmode=disable"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled migration must preserve cancellation: %v", err)
	}
}
