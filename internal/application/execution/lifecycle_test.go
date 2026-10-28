package execution_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

func TestAJobWaitsForTheRunnerWhenItIsLeasedOrPendingForAnyReasonButGitHub(t *testing.T) {
	for name, c := range map[string]struct {
		job  execution.Job
		want bool
	}{
		"pending":                     {execution.Job{State: execution.StatePending}, true},
		"pending after a lost lease":  {execution.Job{State: execution.StatePending, Reason: execution.ReasonLeaseExpired}, true},
		"pending after a timeout":     {execution.Job{State: execution.StatePending, Reason: execution.ReasonTimeout}, true},
		"pending while GitHub fails":  {execution.Job{State: execution.StatePending, Reason: execution.ReasonGitHubUnavailable}, false},
		"leased":                      {execution.Job{State: execution.StateLeased}, true},
		"complete":                    {execution.Job{State: execution.StateComplete}, false},
		"unavailable for no executor": {execution.Job{State: execution.StateUnavailable, Reason: execution.ReasonNoExecutor}, false},
		"cancelled":                   {execution.Job{State: execution.StateCancelled, Reason: execution.ReasonClosed}, false},
	} {
		if got := c.job.WaitsForRunner(); got != c.want {
			t.Errorf("%s: WaitsForRunner = %v, want %v", name, got, c.want)
		}
	}
}
