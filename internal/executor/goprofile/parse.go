package goprofile

import "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"

// Parse turns the output and the exit status of one go test invocation into one observation per expected test.
func Parse(stdout []byte, exitCode int, module string, expected []TestID) (map[TestID]execution.Observation, error) {
	return nil, errNotImplemented
}

// Reruns is the failing tests that deserve another run.
func Reruns(obs map[TestID]execution.Observation, done, max int) []TestID { return nil }
