package river

import (
	"time"

	riverqueue "github.com/riverqueue/river"
)

// IntegrityKind is the kind of the periodic canonical_integrity job
// (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)").
const IntegrityKind = "canonical_integrity"

// defaultIntegrityInterval is the period of the integrity job.
const defaultIntegrityInterval = time.Hour

// addIntegrity registers the integrity job of config in workers and returns its periodic job (none when config has no integrity job).
func addIntegrity(*riverqueue.Workers, Config) (jobs []*riverqueue.PeriodicJob, err error) {
	return nil, nil
}
