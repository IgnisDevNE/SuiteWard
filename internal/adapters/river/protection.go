package river

import (
	"errors"

	riverqueue "github.com/riverqueue/river"
)

// ProtectionCheckKind is the kind of the periodic protection verification job
// (docs/contracts/runtime.md, "Protection verification job (M1.5)").
const ProtectionCheckKind = "github_protection_check"

// addProtection registers the protection verification job of config in workers
// and returns its periodic job (none when config has no verification).
func addProtection(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.ProtectionCheck == nil {
		return jobs, nil
	}
	return nil, errors.New("protection verification job: not implemented")
}
