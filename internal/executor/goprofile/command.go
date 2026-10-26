package goprofile

import "time"

// Command is the go test invocation of one side, and the whole environment of the sandbox's process.
func Command(tests []TestID, timeout time.Duration, vendor bool) (argv, env []string) {
	return nil, nil
}
