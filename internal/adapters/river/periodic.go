package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// The kinds of the periodic jobs of the GitHub integration; the contract sections are in docs/contracts/runtime.md.
const (
	// ProtectionCheckKind is the protection verification job ("Protection verification job (M1.5)").
	ProtectionCheckKind = "github_protection_check"
	// BootstrapKind is the github_bootstrap job ("Command and bootstrap jobs (M1.6a)").
	BootstrapKind = "github_bootstrap"
	// CommandsKind is the github_commands job ("Command and bootstrap jobs (M1.6a)").
	CommandsKind = "github_commands"
	// ChecksKind is the github_checks job, the single writer of check states ("Check job and publisher (M1.6b)").
	ChecksKind = "github_checks"
	// MergesKind is the github_merges job, the only writer of merge facts ("Merges and integrity jobs (M1.7)").
	MergesKind = "github_merges"
	// IntegrityKind is the canonical_integrity job ("Merges and integrity jobs (M1.7)").
	IntegrityKind = "canonical_integrity"
)

// The periods of the periodic jobs when their Config interval is zero.
const (
	defaultProtectionInterval = 15 * time.Minute // D-PROTECTION
	defaultBootstrapInterval  = 60 * time.Second
	defaultCommandsInterval   = 15 * time.Second
	defaultChecksInterval     = 30 * time.Second
	defaultMergesInterval     = 30 * time.Second
	defaultIntegrityInterval  = time.Hour
)

type (
	protectionCheckArgs struct{}
	bootstrapArgs       struct{}
	commandsArgs        struct{}
	checksArgs          struct{}
	mergesArgs          struct{}
	integrityArgs       struct{}
)

func (protectionCheckArgs) Kind() string { return ProtectionCheckKind }
func (bootstrapArgs) Kind() string       { return BootstrapKind }
func (commandsArgs) Kind() string        { return CommandsKind }
func (checksArgs) Kind() string          { return ChecksKind }
func (mergesArgs) Kind() string          { return MergesKind }
func (integrityArgs) Kind() string       { return IntegrityKind }

// runFunc is one run of a periodic job; the kinds it returns are woken after the run ends, whatever its error.
type runFunc func(ctx context.Context) (Wake, error)

// plain adapts a run that wakes nothing; a nil run stays nil.
func plain(run func(ctx context.Context) error) runFunc {
	if run == nil {
		return nil
	}
	return func(ctx context.Context) (Wake, error) { return nil, run(ctx) }
}

// periodicWorker is the worker of every periodic job: it runs the job, then wakes what the run asked for.
type periodicWorker[A riverqueue.JobArgs] struct {
	riverqueue.WorkerDefaults[A]
	run   runFunc
	waker *waker
}

func (w *periodicWorker[A]) Work(ctx context.Context, _ *riverqueue.Job[A]) error {
	wake, err := w.run(ctx)
	w.waker.wake(ctx, wake)
	return err
}

// periodicSpec describes one periodic job of Config: how to find its run and period, how to register its worker, and
// which queue it runs on. Queues, wake-ups and periodic jobs are all generated from the table below.
type periodicSpec struct {
	args         riverqueue.JobArgs // the zero value of the job's args; its Kind names the job
	label        string             // names the job in the error of its registration
	defaultEvery time.Duration
	queued       bool // runs on a queue of one worker named after its kind; the default queue otherwise
	wakeable     bool // a run may wake it (it must be queued: a wake-up inserts on the queue of its kind)
	config       func(Config) (run runFunc, every time.Duration)
	register     func(*riverqueue.Workers, runFunc, *waker) error
}

// spec completes s for the args type A.
func spec[A riverqueue.JobArgs](s periodicSpec) periodicSpec {
	s.args = *new(A)
	s.register = func(workers *riverqueue.Workers, run runFunc, waker *waker) error {
		return riverqueue.AddWorkerSafely(workers, &periodicWorker[A]{run: run, waker: waker})
	}
	return s
}

// periodicSpecs lists the periodic jobs of Config; the order is the order of registration.
var periodicSpecs = []periodicSpec{
	spec[syncTickArgs](periodicSpec{label: "the synchronization tick", defaultEvery: defaultSyncInterval, queued: true,
		config: func(c Config) (runFunc, time.Duration) { return c.SyncTick, c.SyncInterval }}),
	spec[protectionCheckArgs](periodicSpec{label: "the protection verification", defaultEvery: defaultProtectionInterval,
		config: func(c Config) (runFunc, time.Duration) { return plain(c.ProtectionCheck), c.ProtectionInterval }}),
	spec[bootstrapArgs](periodicSpec{label: "the bootstrap", defaultEvery: defaultBootstrapInterval, queued: true, wakeable: true,
		config: func(c Config) (runFunc, time.Duration) { return c.Bootstrap, c.BootstrapInterval }}),
	spec[commandsArgs](periodicSpec{label: "the commands", defaultEvery: defaultCommandsInterval, queued: true, wakeable: true,
		config: func(c Config) (runFunc, time.Duration) { return c.Commands, c.CommandsInterval }}),
	spec[checksArgs](periodicSpec{label: "the checks", defaultEvery: defaultChecksInterval, queued: true, wakeable: true,
		config: func(c Config) (runFunc, time.Duration) { return plain(c.Checks), c.ChecksInterval }}),
	spec[mergesArgs](periodicSpec{label: "the merges job", defaultEvery: defaultMergesInterval, queued: true, wakeable: true,
		config: func(c Config) (runFunc, time.Duration) { return c.Merges, c.MergesInterval }}),
	spec[integrityArgs](periodicSpec{label: "the integrity job", defaultEvery: defaultIntegrityInterval,
		config: func(c Config) (runFunc, time.Duration) { return plain(c.Integrity), c.IntegrityInterval }}),
}

// wakeable is the job kinds a run may wake.
var wakeable = func() map[string]riverqueue.JobArgs {
	kinds := map[string]riverqueue.JobArgs{}
	for _, s := range periodicSpecs {
		if s.wakeable {
			kinds[s.args.Kind()] = s.args
		}
	}
	return kinds
}()

// queues is the River queue configuration: the default queue, the outbox and one worker for each queued periodic job.
func queues(workers int) map[string]riverqueue.QueueConfig {
	configs := map[string]riverqueue.QueueConfig{riverqueue.QueueDefault: {MaxWorkers: workers}, outboxQueue: {MaxWorkers: 1}}
	for _, s := range periodicSpecs {
		if s.queued {
			configs[s.args.Kind()] = riverqueue.QueueConfig{MaxWorkers: 1}
		}
	}
	return configs
}

// addPeriodic registers the worker of s in workers and returns its periodic job (the run is not nil).
func addPeriodic(workers *riverqueue.Workers, s periodicSpec, run runFunc, every time.Duration, waker *waker) (*riverqueue.PeriodicJob, error) {
	if err := s.register(workers, run, waker); err != nil {
		return nil, fmt.Errorf("register %s: %w", s.label, err)
	}
	if every <= 0 {
		every = s.defaultEvery
	}
	kind := s.args.Kind()
	return riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(every),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic runs do not overlap: the insert is unique while a run is unfinished. One that failed is not retried, the next run is.
			opts := &riverqueue.InsertOpts{MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
			if s.queued {
				opts.Queue = kind
			}
			return s.args, opts
		},
		&riverqueue.PeriodicJobOpts{ID: kind, RunOnStart: true}), nil
}

// addPeriodicJobs registers every periodic job that config sets and returns their periodic jobs.
func addPeriodicJobs(workers *riverqueue.Workers, config Config, waker *waker) ([]*riverqueue.PeriodicJob, error) {
	var jobs []*riverqueue.PeriodicJob
	for _, s := range periodicSpecs {
		run, every := s.config(config)
		if run == nil {
			continue
		}
		job, err := addPeriodic(workers, s, run, every, waker)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}
