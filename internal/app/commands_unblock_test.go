package app

import (
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// /suiteward unblock <code> (docs/contracts/runtime.md, "Commands"): after its code is verified and consumed, the owner lifts the jobs of the current head that
// are unavailable now: the canonical job when the canonical level is not off, and the receipt job when the receipt level is not off and the receipt is
// applicable. A refusal keeps the code consumed.

const (
	unblockCommand = "unblock"
	otherHead      = "4444444444444444444444444444444444444444"
)

// requireStepsConsumed requires that exactly the comments were given their step.
func (x *cjExecWorld) requireStepsConsumed(comments ...int64) {
	x.t.Helper()
	var want []contract.SourceCommandID
	for _, c := range comments {
		want = append(want, commentSource(c))
	}
	if !slices.Equal(x.owners.consumed, want) {
		x.t.Fatalf("consumed steps %v; want %v", x.owners.consumed, want)
	}
}

// requireUnblocks requires the rows of the unblock of the comment to name exactly the jobs.
func (x *cjExecWorld) requireUnblocks(comment int64, jobs ...execution.Job) {
	x.t.Helper()
	var want []int64
	for _, j := range jobs {
		want = append(want, j.ID)
	}
	slices.Sort(want)
	got, err := x.store.Unblocks(x.t.Context(), cjID2, comment)
	if err != nil {
		x.t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		x.t.Fatalf("comment %d unblocked jobs %v; want %v", comment, got, want)
	}
}

func TestUnblockLiftsTheUnavailableJobsOfTheHead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		jobs   func(x *cjExecWorld) (lifted, kept []execution.Job)
		plans  []string
		reason string
	}{
		{"the canonical and the receipt job", func(x *cjExecWorld) ([]execution.Job, []execution.Job) {
			return []execution.Job{x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured), x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNoMergeBase)}, nil
		}, []string{"canonical", "receipt"}, ""},
		{"the canonical job alone, with the receipt pending", func(x *cjExecWorld) ([]execution.Job, []execution.Job) {
			return []execution.Job{x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonGitHubUnavailable)}, []execution.Job{x.pending(execution.PlanReceipt, cjHead)}
		}, []string{"canonical"}, ""},
		{"the receipt job alone, with the canonical pending", func(x *cjExecWorld) ([]execution.Job, []execution.Job) {
			return []execution.Job{x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)}, []execution.Job{x.pending(execution.PlanCanonical, cjHead)}
		}, []string{"receipt"}, ""},
		{"only the jobs of the head", func(x *cjExecWorld) ([]execution.Job, []execution.Job) {
			return []execution.Job{x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)}, []execution.Job{x.unavailable(execution.PlanCanonical, otherHead, execution.ReasonNotConfigured)}
		}, []string{"canonical"}, ""},
		{"a complete receipt is not lifted", func(x *cjExecWorld) ([]execution.Job, []execution.Job) {
			return []execution.Job{x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)}, []execution.Job{x.receiptJob(nil, 0, nil)}
		}, []string{"canonical"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCjExecWorld(t, "required", "required", true)
			lifted, kept := tc.jobs(x)
			x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

			wake, err := x.job.run(t.Context())
			if err != nil {
				t.Fatalf("run: %v\nlogs: %s", err, x.logs)
			}

			entry := x.requireEntry(500, commands.Unblocked, map[string]any{"plans": tc.plans})
			if entry.Operation != "" || entry.Mode != "" {
				t.Errorf("entry has operation %q and mode %q; want none", entry.Operation, entry.Mode)
			}
			x.requireStepsConsumed(500)
			x.requireUnblocks(500, lifted...)
			for _, j := range lifted {
				if unblocked, err := x.store.Unblocked(t.Context(), j.ID, cjOrigin); err != nil || !unblocked {
					t.Errorf("Unblocked(%d) = %v, %v; want true", j.ID, unblocked, err)
				}
			}
			for _, j := range kept {
				if unblocked, err := x.store.Unblocked(t.Context(), j.ID, cjOrigin); err != nil || unblocked {
					t.Errorf("Unblocked(%d) = %v, %v; want false: a job that was not unavailable is not lifted", j.ID, unblocked, err)
				}
			}
			if !slices.Contains(wake, river.ChecksKind) {
				t.Errorf("the run woke %v; want the checks, which state that the head was unblocked", wake)
			}
			x.requireNothingLeaked(cjCode(cjAt(10), 0))
		})
	}
}

// Only the job that is current for its plan is lifted: an older job that a newer one replaced, and a plan whose level is off or whose receipt does not apply,
// are not.
func TestUnblockLiftsNothingThatIsNotWantedNow(t *testing.T) {
	t.Run("an older unavailable job that a newer pending one replaced", func(t *testing.T) {
		x := newCjExecWorld(t, "required", "off", true)
		old := x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
		newer := x.pending(execution.PlanCanonical, cjHead) // the fresh job that replaces one created for executor-not-configured
		x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

		x.mustRun()

		x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
		x.requireUnblocks(500)
		for _, j := range []execution.Job{old, newer} {
			if unblocked, err := x.store.Unblocked(t.Context(), j.ID, cjOrigin); err != nil || unblocked {
				t.Errorf("Unblocked(%d) = %v, %v; want false", j.ID, unblocked, err)
			}
		}
	})
	t.Run("a receipt that does not apply to the revision", func(t *testing.T) {
		x := newCjExecWorld(t, "off", "required", false)
		stale := x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)
		x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

		x.mustRun()

		x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
		if unblocked, err := x.store.Unblocked(t.Context(), stale.ID, cjOrigin); err != nil || unblocked {
			t.Errorf("Unblocked(%d) = %v, %v; want false", stale.ID, unblocked, err)
		}
	})
	t.Run("a plan whose level is off", func(t *testing.T) {
		x := newCjExecWorld(t, "off", "required", true)
		spec := x.spec(execution.PlanCanonical, cjHead, true)
		spec.CanonicalLevel = domain.CanonicalLevelAdvisory // the job was created before the policy turned the canonical run off
		if _, _, err := x.store.Enqueue(t.Context(), execution.NewJob{Spec: spec, Unavailable: execution.ReasonNotConfigured, At: cjOrigin.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		receipt := x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)
		x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

		x.mustRun()

		x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"receipt"}})
		x.requireUnblocks(500, receipt)
	})
}

// A refusal keeps the code consumed, records no row, and is no command for the check.
func TestUnblockIsRefusedAndKeepsTheCodeConsumed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		setup  func(x *cjExecWorld, unblock cjComment)
	}{
		{"a fork", "fork", func(x *cjExecWorld, unblock cjComment) {
			x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
			x.headRepo = "evil/gadgets"
			x.sync(unblock)
		}},
		{"a pull request that closed after the comment", "not-open", func(x *cjExecWorld, unblock cjComment) {
			x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
			x.sync(unblock)
			x.closePull(cjID2, cjSync.Add(time.Minute))
		}},
		{"a fork outranks a closed pull request", "fork", func(x *cjExecWorld, unblock cjComment) {
			x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
			x.headRepo = "evil/gadgets"
			x.sync(unblock)
			x.closePull(cjID2, cjSync.Add(time.Minute))
		}},
		{"no job at all", "not-unavailable", func(x *cjExecWorld, unblock cjComment) { x.sync(unblock) }},
		{"jobs that are pending and complete, with a runner that responds", "runner-responding", func(x *cjExecWorld, unblock cjComment) {
			x.receiptJob(nil, 0, nil) // first: a lease takes the oldest pending job
			x.pending(execution.PlanCanonical, cjHead)
			x.sync(unblock)
		}},
		{"an unavailable job of another head only", "not-unavailable", func(x *cjExecWorld, unblock cjComment) {
			x.unavailable(execution.PlanCanonical, otherHead, execution.ReasonNotConfigured)
			x.sync(unblock)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCjExecWorld(t, "required", "required", true)
			tc.setup(x, x.sayWithCode(500, 10, unblockCommand, 0))

			x.mustRun()

			entry := x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": tc.reason})
			if entry.Operation != "" {
				t.Errorf("entry has the operation %q; want none", entry.Operation)
			}
			x.requireStepsConsumed(500)
			x.requireUnblocks(500)
			if _, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0); err != nil || found {
				t.Errorf("LatestOwnerCommand found %v, error %v; a refused unblock is no command for the check", found, err)
			}
			x.requireNothingLeaked(cjCode(cjAt(10), 0))
		})
	}
}

// With no canonical, or a policy that runs nothing, no job of the head can be unavailable.
func TestUnblockIsRefusedWhereNothingRuns(t *testing.T) {
	t.Run("a Suite that has no canonical yet", func(t *testing.T) {
		w := newCjWorld(t)
		w.syncAll([]cjComment{{pull: cjPull, id: 500, author: cjOwnerID, created: cjAt(10), updated: cjAt(10), body: "/suiteward unblock " + cjCode(cjAt(10), 0)}})

		w.mustRun()

		w.requireDisposition(cjID1, 500, commands.UnblockRefused)
		w.requireAckPayload(cjID1, 500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
		if len(w.owners.consumed) != 1 {
			t.Errorf("consumed %d steps; want the code of the refused unblock used", len(w.owners.consumed))
		}
	})
	t.Run("a project that has no Suite", func(t *testing.T) {
		w := newCjWorld(t)
		created := cjAt(10)
		w.syncAll(nil, cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward unblock " + cjCode(created, 0)})

		w.mustRun()

		w.requireDisposition(cjID2, 500, commands.UnblockRefused)
		w.requireAckPayload(cjID2, 500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
	})
	t.Run("a policy whose levels are off", func(t *testing.T) {
		x := newCjExecWorld(t, "off", "off", true)
		x.sync(x.sayWithCode(500, 10, unblockCommand, 0))

		x.mustRun()

		x.requireEntry(500, commands.UnblockRefused, map[string]any{"reason": "not-unavailable"})
	})
}

// A wrong code is invalid-code and a reused one replayed-code, as for any command that carries a code: each sets the owner's rejected command.
func TestUnblockWithAWrongOrReusedCodeIsRejectedLikeAnyCommandWithACode(t *testing.T) {
	t.Run("a wrong code", func(t *testing.T) {
		x := newCjExecWorld(t, "required", "required", true)
		job := x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
		x.sync(x.sayWithCode(500, 10, unblockCommand, 5))

		x.mustRun()

		x.requireEntry(500, commands.InvalidCode, map[string]any{"disposition": "invalid-code"})
		x.requireStepsConsumed()
		x.requireUnblocks(500)
		latest, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0)
		if err != nil || !found || latest.CommentID != 500 || latest.Disposition != commands.InvalidCode {
			t.Errorf("LatestOwnerCommand = %+v, %v, %v; want the invalid code, which sets owner-command-rejected", latest, found, err)
		}
		if unblocked, err := x.store.Unblocked(t.Context(), job.ID, cjOrigin); err != nil || unblocked {
			t.Errorf("Unblocked = %v, %v; want false", unblocked, err)
		}
	})
	t.Run("a reused code", func(t *testing.T) {
		x := newCjExecWorld(t, "required", "required", true)
		x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
		x.sync(x.sayWithCode(500, 10, unblockCommand, 0), x.sayWithCode(501, 10, unblockCommand, 0))

		x.mustRun()

		x.requireEntry(500, commands.Unblocked, map[string]any{"plans": []string{"canonical"}})
		x.requireEntry(501, commands.ReplayedCode, map[string]any{"disposition": "replayed-code"})
		latest, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0)
		if err != nil || !found || latest.CommentID != 501 || latest.Disposition != commands.ReplayedCode {
			t.Errorf("LatestOwnerCommand = %+v, %v, %v; want the replayed code", latest, found, err)
		}
	})
}

// The guard for a closed pull request covers unblock as it covers approve: one first observed after the pull request closed is ignored, with no step and no
// acknowledgment.
func TestAnUnblockFirstObservedAfterThePullRequestClosedIsIgnored(t *testing.T) {
	closedAt := cjSync.Add(10 * time.Minute)
	for name, observedAt := range map[string]time.Time{"after": closedAt.Add(time.Minute), "at the same time": closedAt} {
		t.Run(name, func(t *testing.T) {
			x := newCjExecWorld(t, "required", "required", true)
			x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
			x.observe(cjID2, observedAt, x.sayWithCode(500, 10, unblockCommand, 0))
			x.observe(cjID1, cjSync.Add(20*time.Minute))
			x.closePull(cjID2, closedAt)
			x.closePull(cjID2, cjSync.Add(20*time.Minute))

			x.mustRun()

			x.requireEntry(500, commands.NotCommand, nil)
			x.requireStepsConsumed()
			x.requireUnblocks(500)
		})
	}
}

// None of the five dispositions of the execution commands counts as the owner's latest command, so none of them clears the rejection of an earlier wrong code.
func TestTheExecutionDispositionsNeitherSetNorClearTheRejectedCommand(t *testing.T) {
	x := newCjExecWorld(t, "required", "required", true)
	x.unavailable(execution.PlanReceipt, cjHead, execution.ReasonNotConfigured)
	x.unavailable(execution.PlanCanonical, cjHead, execution.ReasonNotConfigured)
	x.sync(
		x.sayWithCode(500, 10, "approve "+cjExecProposal+" fix", 5), // invalid-code: sets owner-command-rejected
		x.say(501, 11, "mode fix"),                                  // mode-set
		x.sayWithCode(502, 12, "approve "+cjExecProposal+" fix", 0), // receipt-refused: the receipt is unavailable
		x.sayWithCode(503, 13, unblockCommand, 0),                   // unblocked
	)

	x.mustRun()

	for comment, want := range map[int64]commands.Disposition{500: commands.InvalidCode, 501: commands.ModeSet, 502: commands.ReceiptRefused, 503: commands.Unblocked} {
		if entry, _ := x.entry(comment); entry.Disposition != want {
			t.Errorf("comment %d is %q; want %q", comment, entry.Disposition, want)
		}
	}
	latest, found, err := x.ledger.LatestOwnerCommand(t.Context(), cjID2, cjPull, cjOwnerID, 0)
	if err != nil || !found || latest.CommentID != 500 || latest.Disposition != commands.InvalidCode {
		t.Fatalf("LatestOwnerCommand = %+v, %v, %v; want the invalid code of comment 500: none of the five dispositions counts", latest, found, err)
	}
	for _, d := range []commands.Disposition{commands.ReceiptRefused, commands.ModeSet, commands.ModeRefused, commands.Unblocked, commands.UnblockRefused} {
		if d.AffectsCheck() {
			t.Errorf("%q affects the check; want it ignored", d)
		}
	}
}
