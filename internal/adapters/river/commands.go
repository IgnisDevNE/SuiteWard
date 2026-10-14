package river

import (
	riverqueue "github.com/riverqueue/river"
)

// CommandsKind is the kind of the periodic github_commands job
// (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
const CommandsKind = "github_commands"

// addCommands registers the commands job of config in workers and returns its periodic job (none when config has no commands).
func addCommands(workers *riverqueue.Workers, config Config) (jobs []*riverqueue.PeriodicJob, err error) {
	return nil, nil
}
