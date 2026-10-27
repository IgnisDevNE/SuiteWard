package app

import (
	"testing"

	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The shortest EXECUTOR_JOB_TIMEOUT (config) is derived from the test timeout of the domain, so the limit of the lease answer must be that one.
func TestTheLeaseLimitsGiveGoTestTheTimeoutThatTheJobTimeoutIsBuiltOn(t *testing.T) {
	if jobLimits.TestTimeoutSeconds != domain.TestTimeoutSeconds {
		t.Fatalf("the lease answer gives go test %d s, the minimum job timeout is built on %d s", jobLimits.TestTimeoutSeconds, domain.TestTimeoutSeconds)
	}
}
