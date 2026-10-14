package river

import (
	"time"

	riverqueue "github.com/riverqueue/river"
)

// BootstrapKind is the kind of the periodic github_bootstrap job
// (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
const BootstrapKind = "github_bootstrap"

// bootstrapInterval is the period of the bootstrap job.
const bootstrapInterval = 60 * time.Second

// addBootstrap registers the bootstrap job of config in workers and returns its periodic job (none when config has no bootstrap).
func addBootstrap(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	return nil, nil
}
