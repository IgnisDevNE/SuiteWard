package executiontest

import (
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The read of the unblock rows of a head (docs/contracts/persistence.md, "Table execution_unblocks"), and the derived `ended` of a job that reads them
// (docs/contracts/runtime.md, "The early unblock and its hold"): the rows that the commands of the current enrollment wrote for the jobs of the head, each with the
// time its command decided it, and the rule that a job whose end is the time of its row was ended by that command.

// requireHeadUnblocks requires the rows of the head of pull 1 of repository 10 since the time to be want, in the order of the job ids and then of the times.
func (e *env) requireHeadUnblocks(head string, since time.Duration, want ...execution.JobUnblock) {
	e.t.Helper()
	got, err := e.s.HeadUnblocks(e.ctx, 10, 1, head, at(since))
	e.must(err)
	if !slices.EqualFunc(got, want, func(a, b execution.JobUnblock) bool { return a.JobID == b.JobID && a.DecidedAt.Equal(b.DecidedAt) }) {
		e.t.Fatalf("HeadUnblocks(head %s, since %v) = %+v; want %+v", head[len(head)-2:], since, got, want)
	}
}

func rowOf(job execution.Job, decided time.Duration) execution.JobUnblock {
	return execution.JobUnblock{JobID: job.ID, DecidedAt: at(decided)}
}

func ubHeadUnblocks(e *env) {
	// Head 1 has a job that the sweep ended before the owner unblocked it, and a pending job that the unblock ends. Head 2 has a job of its own.
	earlier := canonicalSpec(1)
	earlier.GoverningDigest = digest("an earlier manifest")
	sweptJob := e.enqueue(earlier, 0)
	if n := e.expire(expireParams(10*time.Minute, 0)); n != 1 {
		e.t.Fatalf("Expire changed %d jobs; want the one pending job, ended for no-executor", n)
	}
	swept := e.get(sweptJob.ID)
	pending := e.enqueue(canonicalSpec(1), 11*time.Minute)
	other := e.enqueue(canonicalSpec(2), 11*time.Minute)
	e.requireHeadUnblocks(sha(1), 0)

	// One command lifts both jobs of head 1, with a time that has more than a microsecond; another lifts the job of head 2; a later one lifts the swept job again.
	first := ledgerEntry(1, commands.Unblocked, 12*time.Minute)
	e.must(e.s.RecordUnblock(e.ctx, first, nil, sha(1), unblockJobs(swept, pending), at(12*time.Minute+time.Second+789*time.Nanosecond)))
	second := ledgerEntry(2, commands.Unblocked, 12*time.Minute)
	e.must(e.s.RecordUnblock(e.ctx, second, nil, sha(2), unblockJobs(other), second.DecidedAt))
	third := ledgerEntry(3, commands.Unblocked, 20*time.Minute)
	e.must(e.s.RecordUnblock(e.ctx, third, nil, sha(1), unblockJobs(swept), third.DecidedAt))

	// Each row has the time its command decided it at the precision that is stored, and a job that two commands lifted has two rows.
	decided, decidedLater := 12*time.Minute+time.Second, 20*time.Minute+time.Second
	e.requireHeadUnblocks(sha(1), 0, rowOf(swept, decided), rowOf(swept, decidedLater), rowOf(pending, decided))
	e.requireHeadUnblocks(sha(2), 0, rowOf(other, decided))
	// Only the commands whose entry was created at or after since count, and a head with no row has none.
	e.requireHeadUnblocks(sha(1), 12*time.Minute, rowOf(swept, decided), rowOf(swept, decidedLater), rowOf(pending, decided))
	e.requireHeadUnblocks(sha(1), 12*time.Minute+time.Microsecond, rowOf(swept, decidedLater))
	e.requireHeadUnblocks(sha(1), 20*time.Minute+time.Microsecond)
	e.requireHeadUnblocks(sha(3), 0)
	if got, err := e.s.HeadUnblocks(e.ctx, 10, 2, sha(1), at(0)); err != nil || len(got) != 0 {
		e.t.Fatalf("HeadUnblocks of another pull = %+v, %v; want none", got, err)
	}
	if got, err := e.s.HeadUnblocks(e.ctx, 11, 1, sha(1), at(0)); err != nil || len(got) != 0 {
		e.t.Fatalf("HeadUnblocks of another repository = %+v, %v; want none", got, err)
	}

	// What the command ended is the job whose end is the time of its row: the job that the sweep ended earlier has a row too, and was not ended by it.
	rows, err := e.s.HeadUnblocks(e.ctx, 10, 1, sha(1), at(0))
	e.must(err)
	for name, c := range map[string]struct {
		job  execution.Job
		want bool
	}{"the job the command ended": {e.get(pending.ID), true}, "the job the sweep ended": {e.get(swept.ID), false}, "a job of another head": {e.get(other.ID), false}} {
		if got := c.job.EndedBy(rows); got != c.want {
			e.t.Errorf("%s: EndedBy(%+v) = %v; want %v", name, rows, got, c.want)
		}
	}
	if otherRows, err := e.s.HeadUnblocks(e.ctx, 10, 1, sha(2), at(0)); err != nil || !e.get(other.ID).EndedBy(otherRows) {
		e.t.Errorf("EndedBy(%+v) of the job of head 2 = false, error %v; want true", otherRows, err)
	}
}
