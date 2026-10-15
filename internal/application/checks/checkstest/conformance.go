package checkstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Env is what the conformance suite needs: the store under test, the keys of
// the committed outbox messages and a way to end a pending message as failed
// at a given time, as the relay does once its attempts are used up.
type Env struct {
	Store       checks.Store
	OutboxKeys  func(ctx context.Context) ([]string, error) // sorted
	FailMessage func(ctx context.Context, key string, finishedAt time.Time) error
}

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cooldownMinutes = 15 // checks.RepublishCooldown in minutes, written out so the suite pins the contract's number
)

// at is a time at microsecond precision, which is all PostgreSQL stores, n minutes after a fixed origin.
func at(n int) time.Time {
	return time.Date(2026, 10, 14, 12, 0, 0, 123456000, time.UTC).Add(time.Duration(n) * time.Minute)
}

func key(repository int64, pull int, generation int64) string {
	return fmt.Sprintf("check:%d:%d:%d", repository, pull, generation)
}

// message builds the outbox message of a generation the way the job does.
func message(repository int64, pull int) func(int64) (governance.OutboxMessage, error) {
	return func(generation int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: key(repository, pull, generation), Kind: "github_check", Payload: json.RawMessage(fmt.Sprintf(`{"generation":%d}`, generation))}, nil
	}
}

func result(repository int64, pull int, head string, state checks.State, reason string) checks.Result {
	return checks.Result{RepositoryID: repository, PullNumber: pull, HeadSHA: head, State: state, Reason: reason}
}

type env struct {
	t   *testing.T
	ctx context.Context
	env Env
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) requireErr(err, want error) {
	e.t.Helper()
	if !errors.Is(err, want) {
		e.t.Fatalf("error = %v; want %v", err, want)
	}
}

// publish publishes r with the standard message and requires it to succeed.
func (e *env) publish(r checks.Result, minute int) (checks.Record, bool) {
	e.t.Helper()
	record, written, err := e.env.Store.Publish(e.ctx, r, at(minute), message(r.RepositoryID, r.PullNumber))
	e.must(err)
	return record, written
}

func (e *env) requireOutbox(want ...string) {
	e.t.Helper()
	got, err := e.env.OutboxKeys(e.ctx)
	e.must(err)
	if !slices.Equal(got, want) {
		e.t.Fatalf("outbox keys = %v; want %v", got, want)
	}
}

func (e *env) requireCurrent(repository int64, pull int, want checks.Record) {
	e.t.Helper()
	got, found, err := e.env.Store.Current(e.ctx, repository, pull)
	e.must(err)
	if !found || got != want {
		e.t.Fatalf("Current = %+v, found %v;\nwant %+v", got, found, want)
	}
}

func (e *env) requireAbsent(repository int64, pull int) {
	e.t.Helper()
	if got, found, err := e.env.Store.Current(e.ctx, repository, pull); err != nil || found {
		e.t.Fatalf("Current = %+v, found %v, error %v; want no record", got, found, err)
	}
}

func countTrue(flags []bool) int {
	n := 0
	for _, flag := range flags {
		if flag {
			n++
		}
	}
	return n
}

func (e *env) requirePublished(got checks.Record, written, wantWritten bool, generation int64) {
	e.t.Helper()
	if written != wantWritten || got.Generation != generation {
		e.t.Fatalf("Publish = generation %d, message written %v; want generation %d, written %v", got.Generation, written, generation, wantWritten)
	}
}

// RunConformance runs the checks.Store conformance suite against the environments made by newEnv.
func RunConformance(t *testing.T, newEnv func(*testing.T) Env) {
	run := func(name string, test func(*env)) {
		t.Run(name, func(t *testing.T) { test(&env{t: t, ctx: t.Context(), env: newEnv(t)}) })
	}

	run("Current finds nothing before the first Publish", func(e *env) {
		e.requireAbsent(10, 1)
	})

	run("the first Publish writes generation 1 and one message", func(e *env) {
		r := result(10, 1, headA, checks.InProgress, "approval-missing")
		r.Reference, r.Change, r.Revision, r.CommandID = "P1-R2", "protected-change", 4, 77
		record, written := e.publish(r, 1)
		e.requirePublished(record, written, true, 1)
		want := checks.Record{Result: r, HeadSince: at(1), Generation: 1, DecidedAt: at(1)}
		if record != want {
			e.t.Fatalf("Publish returned %+v; want %+v", record, want)
		}
		e.requireCurrent(10, 1, want)
		e.requireOutbox(key(10, 1, 1))
	})

	run("an identical result writes no message and updates what the verdict carries", func(e *env) {
		first := result(10, 1, headA, checks.InProgress, "approval-missing")
		e.publish(first, 1)
		again := first
		again.Change, again.Revision, again.CommandID = "protected-change", 9, 55
		record, written := e.publish(again, 5)
		e.requirePublished(record, written, false, 1)
		e.requireCurrent(10, 1, checks.Record{Result: again, HeadSince: at(1), Generation: 1, DecidedAt: at(5)})
		e.requireOutbox(key(10, 1, 1))
	})

	run("a new head, state, reason or reference advances the generation by exactly one", func(e *env) {
		current := result(10, 1, headA, checks.InProgress, "approval-missing")
		current.Reference = "P1-R1"
		e.publish(current, 1)
		since := at(1) // only a new head restarts it
		for i, step := range []struct {
			name   string
			mutate func(*checks.Result)
		}{
			{"head", func(r *checks.Result) { r.HeadSHA = headB }},
			{"state", func(r *checks.Result) { r.State = checks.Failure }},
			{"reason", func(r *checks.Result) { r.Reason = "invalid-source" }},
			{"reference", func(r *checks.Result) { r.Reference = "P1-R2" }},
		} {
			minute := 2 + i
			step.mutate(&current)
			if step.name == "head" {
				since = at(minute)
			}
			record, written := e.publish(current, minute)
			e.requirePublished(record, written, true, int64(i+2))
			if record.HeadSince != since || record.DecidedAt != at(minute) {
				e.t.Fatalf("after a new %s: head since %v, decided at %v; want %v, %v", step.name, record.HeadSince, record.DecidedAt, since, at(minute))
			}
		}
		e.requireOutbox(key(10, 1, 1), key(10, 1, 2), key(10, 1, 3), key(10, 1, 4), key(10, 1, 5))
	})

	run("the message is built for the generation it publishes", func(e *env) {
		var seen []int64
		build := func(generation int64) (governance.OutboxMessage, error) {
			seen = append(seen, generation)
			return message(10, 1)(generation)
		}
		for i, head := range []string{headA, headA, headB} { // the second result is identical to the first
			_, _, err := e.env.Store.Publish(e.ctx, result(10, 1, head, checks.Success, "ready"), at(i+1), build)
			e.must(err)
		}
		if !slices.Equal(seen, []int64{1, 2}) {
			e.t.Fatalf("message was built for generations %v; want [1 2]", seen)
		}
	})

	run("a publication that ended failed is retried under the next generation once the cooldown has passed", func(e *env) {
		r := result(10, 1, headA, checks.InProgress, "approval-missing")
		e.publish(r, 1)
		e.must(e.env.FailMessage(e.ctx, key(10, 1, 1), at(1)))
		record, written := e.publish(r, 1+cooldownMinutes)
		e.requirePublished(record, written, true, 2)
		if record.HeadSince != at(1) || record.DecidedAt != at(1+cooldownMinutes) {
			e.t.Fatalf("head since %v, decided at %v; want %v, %v", record.HeadSince, record.DecidedAt, at(1), at(1+cooldownMinutes))
		}
		e.requireOutbox(key(10, 1, 1), key(10, 1, 2))
		// The new message is pending: another identical result is quiet again.
		record, written = e.publish(r, 2+cooldownMinutes)
		e.requirePublished(record, written, false, 2)
		// Only the current generation's message matters: an earlier failure does not repeat the retry.
		e.must(e.env.FailMessage(e.ctx, key(10, 1, 2), at(2+cooldownMinutes)))
		next := result(10, 1, headB, checks.Success, "ready")
		record, written = e.publish(next, 3+cooldownMinutes)
		e.requirePublished(record, written, true, 3)
		record, written = e.publish(next, 4+cooldownMinutes)
		e.requirePublished(record, written, false, 3)
	})

	run("a lost publication is not retried before the cooldown has passed", func(e *env) {
		r := result(10, 1, headA, checks.InProgress, "approval-missing")
		e.publish(r, 1)
		e.must(e.env.FailMessage(e.ctx, key(10, 1, 1), at(1)))
		for _, minute := range []int{2, cooldownMinutes} { // the last one is a minute short of the cooldown
			record, written := e.publish(r, minute)
			e.requirePublished(record, written, false, 1)
			if record.DecidedAt != at(minute) {
				e.t.Fatalf("decided at %v; want %v", record.DecidedAt, at(minute))
			}
		}
		e.requireOutbox(key(10, 1, 1))
		// A changed result is not a retry: it publishes inside the cooldown.
		record, written := e.publish(result(10, 1, headB, checks.Success, "ready"), cooldownMinutes)
		e.requirePublished(record, written, true, 2)
		e.requireOutbox(key(10, 1, 1), key(10, 1, 2))
	})

	run("a lost publication is retried once a whole cooldown after it failed, however late it was recorded", func(e *env) {
		r := result(10, 1, headA, checks.InProgress, "approval-missing")
		e.publish(r, 1)
		e.must(e.env.FailMessage(e.ctx, key(10, 1, 1), at(5))) // the relay gave up four minutes after the publication
		record, written := e.publish(r, 4+cooldownMinutes)
		e.requirePublished(record, written, false, 1)
		record, written = e.publish(r, 5+cooldownMinutes)
		e.requirePublished(record, written, true, 2)
	})

	run("pulls and repositories are independent", func(e *env) {
		e.publish(result(10, 1, headA, checks.Success, "ready"), 1)
		e.publish(result(10, 2, headB, checks.Failure, "invalid-source"), 2)
		e.publish(result(20, 1, headB, checks.InProgress, "bootstrap-pending"), 3)
		for _, c := range []struct {
			repository int64
			pull       int
			head       string
			state      checks.State
		}{{10, 1, headA, checks.Success}, {10, 2, headB, checks.Failure}, {20, 1, headB, checks.InProgress}} {
			got, found, err := e.env.Store.Current(e.ctx, c.repository, c.pull)
			e.must(err)
			if !found || got.HeadSHA != c.head || got.State != c.state || got.Generation != 1 {
				e.t.Fatalf("Current(%d, %d) = %+v, found %v; want head %s, state %s, generation 1", c.repository, c.pull, got, found, c.head, c.state)
			}
		}
		e.requireAbsent(20, 2)
		e.requireOutbox(key(10, 1, 1), key(10, 2, 1), key(20, 1, 1))
	})

	// concurrently runs callers Publish calls at once for pull 1 of repository 10, result(i) being the i-th caller's.
	concurrently := func(e *env, callers int, result func(i int) checks.Result) (written []bool) {
		written = make([]bool, callers)
		failures := make([]error, callers)
		var ready, done sync.WaitGroup
		ready.Add(callers)
		start := make(chan struct{})
		for i := range callers {
			done.Add(1)
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				_, written[i], failures[i] = e.env.Store.Publish(e.ctx, result(i), at(1), message(10, 1))
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		for _, err := range failures {
			e.must(err)
		}
		return written
	}

	run("concurrent Publish of one result writes one message", func(e *env) {
		written := concurrently(e, 8, func(int) checks.Result { return result(10, 1, headA, checks.InProgress, "approval-missing") })
		if messages := countTrue(written); messages != 1 {
			e.t.Fatalf("%d callers wrote a message; want exactly one", messages)
		}
		e.requireOutbox(key(10, 1, 1))
	})

	run("concurrent Publish of different results never writes two messages for one generation", func(e *env) {
		const callers = 8
		written := concurrently(e, callers, func(i int) checks.Result {
			return result(10, 1, headA, checks.InProgress, fmt.Sprintf("reason-%d", i))
		})
		messages := countTrue(written)
		current, found, err := e.env.Store.Current(e.ctx, 10, 1)
		e.must(err)
		keys, err := e.env.OutboxKeys(e.ctx)
		e.must(err)
		// Every result differs from the last stored one, so each advances the generation once.
		if !found || messages != callers || int(current.Generation) != callers || len(keys) != callers {
			e.t.Fatalf("generation %d, %d messages written, outbox keys %v; want %d of each", current.Generation, messages, keys, callers)
		}
	})

	run("a failing message rolls the record back", func(e *env) {
		boom := errors.New("cannot build the message")
		failing := func(int64) (governance.OutboxMessage, error) { return governance.OutboxMessage{}, boom }
		first := result(10, 1, headA, checks.InProgress, "approval-missing")

		_, _, err := e.env.Store.Publish(e.ctx, first, at(1), failing)
		e.requireErr(err, boom)
		e.requireAbsent(10, 1)

		// The message is invalid.
		invalid := func(int64) (governance.OutboxMessage, error) {
			return governance.OutboxMessage{Key: "check:10:1:1", Kind: "Bad", Payload: json.RawMessage(`{}`)}, nil
		}
		_, _, err = e.env.Store.Publish(e.ctx, first, at(1), invalid)
		e.requireErr(err, governance.ErrInvalidRequest)
		e.requireAbsent(10, 1)
		e.requireOutbox()

		e.publish(first, 2)
		before := checks.Record{Result: first, HeadSince: at(2), Generation: 1, DecidedAt: at(2)}

		// A new state with a failing message leaves the record as it was.
		_, _, err = e.env.Store.Publish(e.ctx, result(10, 1, headA, checks.Success, "ready"), at(3), failing)
		e.requireErr(err, boom)
		e.requireCurrent(10, 1, before)

		// A message whose key is taken fails the same way.
		_, _, err = e.env.Store.Publish(e.ctx, result(20, 1, headA, checks.Success, "ready"), at(3), message(10, 1)) // key check:10:1:1 exists
		e.requireErr(err, governance.ErrOperationConflict)
		e.requireAbsent(20, 1)

		// The retry of a lost publication that fails changes nothing either.
		e.must(e.env.FailMessage(e.ctx, key(10, 1, 1), at(3)))
		_, _, err = e.env.Store.Publish(e.ctx, first, at(3+cooldownMinutes), failing)
		e.requireErr(err, boom)
		e.requireCurrent(10, 1, before)
		e.requireOutbox(key(10, 1, 1))

		// An update that writes no message never calls it.
		e.publish(first, 4+cooldownMinutes) // the retry succeeds: generation 2
		quiet := first
		quiet.Revision = 3
		record, written, err := e.env.Store.Publish(e.ctx, quiet, at(5+cooldownMinutes), failing)
		e.must(err)
		e.requirePublished(record, written, false, 2)
	})

	run("an invalid result is not recorded", func(e *env) {
		valid := result(10, 1, headA, checks.Success, "ready")
		bad := map[string]func(*checks.Result){
			"repository":     func(r *checks.Result) { r.RepositoryID = 0 },
			"pull zero":      func(r *checks.Result) { r.PullNumber = 0 },
			"huge pull":      func(r *checks.Result) { r.PullNumber = 1 << 31 },
			"short head":     func(r *checks.Result) { r.HeadSHA = "abc123" },
			"uppercase head": func(r *checks.Result) { r.HeadSHA = strings.ToUpper(headA) },
			"non-hex head":   func(r *checks.Result) { r.HeadSHA = strings.Repeat("g", 40) },
			"empty head":     func(r *checks.Result) { r.HeadSHA = "" },
			"empty reason":   func(r *checks.Result) { r.Reason = "" },
			"blank reason":   func(r *checks.Result) { r.Reason = " 	" },
			"neutral":        func(r *checks.Result) { r.State = "neutral" },
			"empty state":    func(r *checks.Result) { r.State = "" },
			"reference":      func(r *checks.Result) { r.Reference = "P1" },
			"reference text": func(r *checks.Result) { r.Reference = "P1-R2 " },
			"revision":       func(r *checks.Result) { r.Revision = -1 },
			"command":        func(r *checks.Result) { r.CommandID = -1 },
		}
		for name, mutate := range bad {
			r := valid
			mutate(&r)
			e.t.Run(name, func(t *testing.T) {
				sub := *e
				sub.t = t
				_, _, err := sub.env.Store.Publish(sub.ctx, r, at(1), message(10, 1))
				sub.requireErr(err, checks.ErrInvalid)
			})
		}
		_, _, err := e.env.Store.Publish(e.ctx, valid, time.Time{}, message(10, 1))
		e.requireErr(err, checks.ErrInvalid)
		for _, c := range []struct {
			repository int64
			pull       int
		}{{0, 1}, {10, 0}} {
			_, _, err := e.env.Store.Current(e.ctx, c.repository, c.pull)
			e.requireErr(err, checks.ErrInvalid)
		}
		e.requireAbsent(10, 1)
		e.requireOutbox()
		// A reference of the contract's form is accepted.
		valid.Reference = "P12-R34"
		e.publish(valid, 2)
	})

	run("a time keeps the precision the store keeps", func(e *env) {
		// PostgreSQL keeps microseconds; the caller's clock has nanoseconds.
		r := result(10, 1, headA, checks.Success, "ready")
		_, written, err := e.env.Store.Publish(e.ctx, r, at(1).Add(789*time.Nanosecond), message(10, 1))
		e.must(err)
		if !written {
			e.t.Fatal("the first result wrote no message")
		}
		e.requireCurrent(10, 1, checks.Record{Result: r, HeadSince: at(1), Generation: 1, DecidedAt: at(1)})
	})

	run("a canceled context writes nothing", func(e *env) {
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		_, _, err := e.env.Store.Publish(ctx, result(10, 1, headA, checks.Success, "ready"), at(1), message(10, 1))
		e.requireErr(err, context.Canceled)
		_, _, err = e.env.Store.Current(ctx, 10, 1)
		e.requireErr(err, context.Canceled)
		e.requireAbsent(10, 1)
		e.requireOutbox()
	})
}
