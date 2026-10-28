package app

import (
	"net/http"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The old lease of a job that the owner's unblock ended (docs/contracts/runtime.md, "The early unblock and its hold"): the job is final for no-executor with no results
// row, so the result that its executor sends later is a conflict and its heartbeat a lost lease, and the executor discards its work.
func TestAResultAndAHeartbeatOfALeaseThatAnUnblockEndedAreRefused(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	entry := commands.Entry{RepositoryID: execRepoID, CommentID: 9, PullNumber: leased.PullNumber, AuthorID: 7, CreatedAt: e.clock.Now(), BodyDigest: artifact.Hash([]byte("body")),
		Disposition: commands.Unblocked, DecidedAt: e.clock.Now()}
	observed := []execution.UnblockJob{{ID: leased.ID, State: execution.StateLeased, LeaseID: leased.LeaseID}}
	if err := e.store.RecordUnblock(t.Context(), entry, nil, leased.HeadSHA, observed, e.clock.Now()); err != nil {
		t.Fatal(err)
	}

	e.wantError(e.post(leased, leased.LeaseID, e.complete(leased)), http.StatusConflict, "conflict")
	e.wantError(e.send(http.MethodPost, e.jobPath(leased, "/heartbeat"), nil, withLease(leased.LeaseID)), http.StatusConflict, "lease-lost")

	if got := e.job(leased.ID); got.State != execution.StateUnavailable || got.Reason != execution.ReasonNoExecutor {
		t.Errorf("job = %s/%s; want it still unavailable for no-executor", got.State, got.Reason)
	}
	if _, found, err := e.store.Result(t.Context(), leased.ID); err != nil || found {
		t.Errorf("result stored = %v, %v; want none", found, err)
	}
	if n := e.wakes.count.Load(); n != 0 {
		t.Errorf("wake-ups = %d; want none", n)
	}
}
