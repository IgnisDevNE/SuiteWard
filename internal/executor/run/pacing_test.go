package run

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The floor of EXECUTOR_JOB_TIMEOUT copies the executor's margin, which the domain cannot import: the two must not drift apart.
func TestTheJobTimeoutFloorLeavesTheExecutorsMarginAfterGoTestsOwnKill(t *testing.T) {
	kill := execution.TestTimeoutSeconds*time.Second + time.Minute
	if left := execution.MinJobTimeout - kill - DefaultPacing.Margin; left < time.Minute {
		t.Errorf("MinJobTimeout %s leaves %s around the test binary after go test's kill (%s) and the margin (%s); want at least a minute",
			execution.MinJobTimeout, left, kill, DefaultPacing.Margin)
	}
}
