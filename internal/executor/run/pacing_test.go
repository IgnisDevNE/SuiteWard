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

// A false trip of the not-responding state posts a comment that is never taken back (ADR 0029, section 5), so a healthy executor must never
// leave the service without a request for execution.NotRespondingAfter. Each gap is a number of the pacing: the service records a request when
// it arrives and when it answers, so a gap runs from the end of one request to the start of the next.
func TestAHealthyExecutorNeverLeavesTheServiceSilentForThirtySeconds(t *testing.T) {
	p := DefaultPacing
	gaps := map[string]time.Duration{
		"between idle lease requests":           p.Idle,
		"between heartbeats":                    p.Heartbeat,
		"between lease requests after an error": p.MaxBackoff,
		"between two posts of a result":         p.Heartbeat, // the pause stops at a heartbeat interval (post in job.go)
	}
	for name, gap := range gaps {
		if gap >= execution.NotRespondingAfter {
			t.Errorf("the gap %s is %v; want less than %v", name, gap, execution.NotRespondingAfter)
		}
	}
	// A crash in the middle of a run leaves the last heartbeat, the supervisor's restart delay and the start-up clean-up. A routine restart,
	// whose clean-up takes at most 15 s (10 s on macOS), does not read as not responding.
	restarts := map[string]struct{ delay, cleanup time.Duration }{
		"Linux and Windows": {5 * time.Second, 15 * time.Second},
		"macOS":             {10 * time.Second, 10 * time.Second},
	}
	for system, r := range restarts {
		if crash := p.Heartbeat + r.delay + r.cleanup; crash > execution.NotRespondingAfter {
			t.Errorf("on %s a crash leaves %v of silence (heartbeat, restart delay %v, clean-up %v); want at most %v", system, crash, r.delay, r.cleanup, execution.NotRespondingAfter)
		}
	}
}
