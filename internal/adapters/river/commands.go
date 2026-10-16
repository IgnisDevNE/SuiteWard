package river

import (
	"context"
	"fmt"
	"time"

	riverqueue "github.com/riverqueue/river"
)

// CommandsKind is the kind of the periodic github_commands job
// (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
const CommandsKind = "github_commands"

// defaultCommandsInterval is the period of the commands job.
const defaultCommandsInterval = 15 * time.Second

type commandsArgs struct{}

func (commandsArgs) Kind() string { return CommandsKind }

type commandsWorker struct {
	riverqueue.WorkerDefaults[commandsArgs]
	run   func(ctx context.Context) (Wake, error)
	waker *waker
}

func (w *commandsWorker) Work(ctx context.Context, _ *riverqueue.Job[commandsArgs]) error {
	wake, err := w.run(ctx)
	w.waker.wake(ctx, wake)
	return err
}

// addCommands registers the commands job of config in workers and returns its periodic job (none when config has no commands).
func addCommands(workers *riverqueue.Workers, config Config, waker *waker) (jobs []*riverqueue.PeriodicJob, err error) {
	if config.Commands == nil {
		return jobs, nil
	}
	if err := riverqueue.AddWorkerSafely(workers, &commandsWorker{run: config.Commands, waker: waker}); err != nil {
		return nil, fmt.Errorf("register the commands: %w", err)
	}
	interval := config.CommandsInterval
	if interval <= 0 {
		interval = defaultCommandsInterval
	}
	run := riverqueue.NewPeriodicJob(riverqueue.PeriodicInterval(interval),
		func() (riverqueue.JobArgs, *riverqueue.InsertOpts) {
			// Periodic runs coalesce while one is unfinished (the kind has a queue of one worker); one that failed is not retried, the next run is.
			return commandsArgs{}, &riverqueue.InsertOpts{Queue: CommandsKind, MaxAttempts: 1, UniqueOpts: riverqueue.UniqueOpts{ByState: uniqueWhileActive}}
		},
		&riverqueue.PeriodicJobOpts{ID: CommandsKind, RunOnStart: true})
	return append(jobs, run), nil
}
