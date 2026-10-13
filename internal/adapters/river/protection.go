package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// ProtectionCheckKind is the kind of the periodic protection verification job
// (docs/contracts/runtime.md, "Protection verification job (M1.5)").
const ProtectionCheckKind = "github_protection_check"

// defaultProtectionInterval is the period of the protection verification (D-PROTECTION).
const defaultProtectionInterval = 15 * time.Minute

type protectionCheckArgs struct{}

func (protectionCheckArgs) Kind() string { return ProtectionCheckKind }

type protectionCheckWorker struct {
	riverqueue.WorkerDefaults[protectionCheckArgs]
	run func(ctx context.Context) error
}

func (w *protectionCheckWorker) Work(ctx context.Context, _ *riverqueue.Job[protectionCheckArgs]) error {
	return w.run(ctx)
}

// addProtection registers the protection verification job of config in workers
// and returns its periodic job (none when config has no verification).
func addProtection(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.ProtectionCheck == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &protectionCheckWorker{run: config.ProtectionCheck}); err != nil {
		return nil, fmt.Errorf("register the protection verification: %w", err)
	}
	interval := config.ProtectionInterval
	if interval <= 0 {
		interval = defaultProtectionInterval
	}
	check := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// A check does not start while an earlier one runs; one that failed is not retried, the next run is.
			return protectionCheckArgs{}, &riverqueue.InsertOpts{MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: ProtectionCheckKind, RunOnStart: true})
	return append(jobs, check), nil
}
