package installer

import (
	"context"
	"io"
)

// doctorResult is one line of `suiteward doctor`: the JSON keys are those of `suiteward check --json`, whose results are
// merged into the same list.
type doctorResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// Doctor is the host half of `suiteward doctor`; it returns the exit code.
func Doctor(ctx context.Context, deps Deps, settingsPath string, out io.Writer, jsonOut bool) int {
	return -1
}
