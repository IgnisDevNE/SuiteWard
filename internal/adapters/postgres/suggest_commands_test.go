//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func TestSuggestCommandDispositionsExistAtTheSupportedVersion(t *testing.T) {
	if migrations.SupportedVersion < 19 {
		t.Fatalf("SupportedVersion = %d; the suggested and suggest-refused dispositions are migration 19", migrations.SupportedVersion)
	}
}

// A database at version 18 refuses the new dispositions and keeps its rows; migration 19 accepts them without an operation id,
// still refuses an unknown one, and leaves the dispositions of 17 as they were.
func TestSuggestCommandMigrationExtendsTheDispositionCheck(t *testing.T) {
	requireDispositionMigration(t, 18, "skip-refused", "suggested", []string{"suggested", "suggest-refused", "proposed", "skipped"}, "suggestions")
}
