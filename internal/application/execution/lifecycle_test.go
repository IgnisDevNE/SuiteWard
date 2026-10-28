package execution_test

import (
	"testing"
	"time"

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

// An unblock ended a job, and not the sweep, when the job is unavailable for no-executor and its end is the time of the row that names it, at the precision that
// is stored (docs/contracts/runtime.md, "The early unblock and its hold").
func TestAJobWasEndedByAnUnblockWhenItsEndIsTheTimeOfItsRow(t *testing.T) {
	end := time.Date(2026, 10, 28, 12, 0, 0, 123456000, time.UTC)
	noExecutor := execution.Job{ID: 7, State: execution.StateUnavailable, Reason: execution.ReasonNoExecutor, FinishedAt: end}
	row := func(id int64, decided time.Time) execution.JobUnblock {
		return execution.JobUnblock{JobID: id, DecidedAt: decided}
	}
	for name, c := range map[string]struct {
		job      execution.Job
		unblocks []execution.JobUnblock
		want     bool
	}{
		"the command ended the job":                      {noExecutor, []execution.JobUnblock{row(7, end)}, true},
		"the job was unavailable already":                {noExecutor, []execution.JobUnblock{row(7, end.Add(time.Minute))}, false},
		"the row is before the end":                      {noExecutor, []execution.JobUnblock{row(7, end.Add(-time.Microsecond))}, false},
		"the times differ below a microsecond":           {noExecutor, []execution.JobUnblock{row(7, end.Add(789*time.Nanosecond))}, true},
		"the row names another job":                      {noExecutor, []execution.JobUnblock{row(8, end)}, false},
		"no row":                                         {noExecutor, nil, false},
		"a later command unblocked the job again":        {noExecutor, []execution.JobUnblock{row(7, end), row(7, end.Add(time.Hour))}, true},
		"an earlier command did not end it, a later did": {noExecutor, []execution.JobUnblock{row(7, end.Add(-time.Hour)), row(7, end)}, true},
		"it ended for another reason":                    {execution.Job{ID: 7, State: execution.StateUnavailable, Reason: execution.ReasonNotConfigured, FinishedAt: end}, []execution.JobUnblock{row(7, end)}, false},
		"it is not unavailable":                          {execution.Job{ID: 7, State: execution.StatePending, FinishedAt: end}, []execution.JobUnblock{row(7, end)}, false},
	} {
		if got := c.job.EndedBy(c.unblocks); got != c.want {
			t.Errorf("%s: EndedBy = %v, want %v", name, got, c.want)
		}
	}
}
