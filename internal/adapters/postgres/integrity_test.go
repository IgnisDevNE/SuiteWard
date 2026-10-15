//go:build integration

package postgres_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
)

func TestIntegrityConformance(t *testing.T) {
	integritytest.RunConformance(t, func(t *testing.T) integrity.Store {
		w := newPGWorld(t)
		// The verdict names a version the database knows: the Suite with v1, and a second version v2.
		seedGovernance(t, w.db.conn)
		mustInsert(t, w.db.conn, "suite_versions", map[string]any{"version_id": string(integritytest.V2)})
		return w.store.Integrity()
	})
}
