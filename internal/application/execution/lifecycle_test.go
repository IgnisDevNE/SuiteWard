package execution_test

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
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

// The checks, the unblock and the comments of a head read one rule for "waits for the runner" (docs/contracts/runtime.md, "Not responding"): the rule of the domain.
// Whatever state and reason a job has, the application job answers as the domain job does; a cancelled job is no part of a set and waits for nothing.
func TestTheApplicationJobAndTheDomainJobAgreeOnWaitingForTheRunner(t *testing.T) {
	states := []execution.State{execution.StatePending, execution.StateLeased, execution.StateComplete, execution.StateUnavailable, execution.StateCancelled}
	reasons := []execution.Reason{"", execution.ReasonExecutorError, execution.ReasonSandboxUnavailable, execution.ReasonArchiveUnavailable, execution.ReasonLeaseExpired,
		execution.ReasonTimeout, execution.ReasonGitHubUnavailable, execution.ReasonNoExecutor, execution.ReasonFetchFailed, execution.ReasonInstallFailed,
		execution.ReasonResourceLimit, execution.ReasonToolchainMismatch, execution.ReasonSourceMismatch, execution.ReasonTooLarge, execution.ReasonProfileUnsupported,
		execution.ReasonNotConfigured, execution.ReasonNoMergeBase, execution.ReasonHeadMoved, execution.ReasonClosed, execution.ReasonObsolete}
	for _, state := range states {
		for _, reason := range reasons {
			want := false
			if inSet, err := domain.ParseJobState(string(state)); err == nil {
				want = domain.Job{ID: 1, State: inSet, Reason: string(reason)}.WaitsForRunner()
			}
			if got := (execution.Job{ID: 1, State: state, Reason: reason}).WaitsForRunner(); got != want {
				t.Errorf("a %s job with the reason %q: WaitsForRunner = %v, the domain rule says %v", state, reason, got, want)
			}
		}
	}
}
