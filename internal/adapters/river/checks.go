package river

import (
	"context"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// ChecksKind is the kind of the periodic github_checks job, the single writer of check states
// (docs/contracts/runtime.md, "Check job and publisher (M1.6b)").
const ChecksKind = "github_checks"

// defaultChecksInterval is the period of the checks job.
const defaultChecksInterval = 30 * time.Second

// addChecks registers the checks job of config in workers and returns its periodic job (none when config has no checks).
func addChecks(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	return jobs, nil
}

var _ = context.Background
