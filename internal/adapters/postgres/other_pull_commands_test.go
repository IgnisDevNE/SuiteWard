//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestOtherPullCommandDispositionExistsAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 20 {
		t.Fatalf("SupportedVersion = %d; the other-pull disposition is migration 20", migrations.SupportedVersion)
	}
}

// A database at version 19 refuses the new disposition and keeps its rows; migration 20 accepts it without an operation id,
// still refuses an unknown one, and leaves the dispositions of 19 as they were.
func TestOtherPullCommandMigrationExtendsTheDispositionCheck(t *testing.T) {
	requireDispositionMigration(t, 19, "suggest-refused", "other-pull", []string{"other-pull", "suggested"}, "other-pulls")
}
