package river

import (
	riverqueue "github.com/riverqueue/river"
)

// MergesKind is the kind of the periodic github_merges job (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)").
const MergesKind = "github_merges"

// addMerges registers the merges job of config in workers and returns its periodic job (none when config has no merges).
func addMerges(_ *riverqueue.Workers, _ Config) (jobs []*riverqueue.PeriodicJob, err error) {
	return jobs, nil
}
