// Package commandstest provides an in-memory commands.Store and the
// conformance suite that every commands.Store must pass.
package commandstest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// Env is what the conformance suite needs: the store under test, the owner
// and observation stores that decide which comments are pending, and the keys
// of the committed outbox messages (the acknowledgments).
type Env struct {
	Store      commands.Store
	Owner      owner.Store
	Sync       githubsync.Store
	OutboxKeys func(ctx context.Context) ([]string, error) // sorted
}

const ownerAccount = int64(884695)

// cmdTime is a time at microsecond precision, which is all PostgreSQL stores,
// n minutes after a fixed origin.
func cmdTime(n int) time.Time {
	return time.Date(2026, 10, 14, 12, 0, 0, 123456000, time.UTC).Add(time.Duration(n) * time.Minute)
}

func repoName(id int64) string { return "acme/repo-" + string(rune('a'+id)) }

type cmdEnv struct {
	t     *testing.T
	ctx   context.Context
	env   Env
	store commands.Store
	steps int64
}

func (e *cmdEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *cmdEnv) requireErr(err, want error) {
	e.t.Helper()
	if !errors.Is(err, want) {
		e.t.Fatalf("error = %v; want %v", err, want)
	}
}

// enroll enrolls the owner over the repositories, each with a fresh TOTP step.
func (e *cmdEnv) enroll(repositories ...int64) {
	e.t.Helper()
	e.steps++
	projects := make([]owner.ProjectPolicy, len(repositories))
	for i, id := range repositories {
		projects[i] = owner.ProjectPolicy{RepositoryID: id, Repo: repoName(id), TestCheck: "ci"}
	}
	e.must(e.env.Owner.Enroll(e.ctx, owner.Enrollment{Owner: owner.Owner{AccountID: ownerAccount, Login: "owner"}, Secret: bytes.Repeat([]byte{1}, 20),
		Projects: projects, Step: e.steps, Tier: owner.TierIsolated, EnrolledAt: cmdTime(0)}))
}

// observed is one comment as one cycle lists it; times are minutes after the origin.
type observed struct {
	pull             int
	id               int64
	created, updated int
	body             string
}

// observe records one successful cycle of the repository at the minute at, listing exactly these comments.
func (e *cmdEnv) observe(repository int64, at int, comments ...observed) {
	e.t.Helper()
	cycle := githubsync.Cycle{Repo: repoName(repository), InstallationID: 1, At: cmdTime(at), Comments: map[int][]githubsync.ObservedComment{}}
	for _, c := range comments {
		if _, listed := cycle.Comments[c.pull]; !listed {
			cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: c.pull, HeadSHA: "head", HeadRef: "feature", BaseRef: "main", BaseSHA: "base", UpdatedAt: cmdTime(at)})
		}
		cycle.Comments[c.pull] = append(cycle.Comments[c.pull], githubsync.ObservedComment{CommentID: c.id, AuthorID: 7, AuthorLogin: "reviewer",
			CreatedAt: cmdTime(c.created), UpdatedAt: cmdTime(c.updated), Body: c.body})
	}
	e.must(e.env.Sync.RecordCycle(e.ctx, cycle))
}

// candidate is the first observed version of a comment, observed at the minute firstSeen.
func candidate(repository int64, firstSeen int, c observed) commands.Candidate {
	return commands.Candidate{RepositoryID: repository, Repo: repoName(repository), PullNumber: c.pull, Comment: githubsync.CommentVersion{
		PullNumber: c.pull, CommentID: c.id, AuthorID: 7, AuthorLogin: "reviewer", CreatedAt: cmdTime(c.created), UpdatedAt: cmdTime(c.updated),
		Body: c.body, BodyDigest: artifact.Hash([]byte(c.body)), FirstSeen: cmdTime(firstSeen)}}
}

func (e *cmdEnv) requirePending(horizon, limit int, want ...commands.Candidate) {
	e.t.Helper()
	got, err := e.store.Pending(e.ctx, cmdTime(horizon), limit)
	e.must(err)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		e.t.Fatalf("Pending = %+v;\nwant %+v", got, want)
	}
}

func (e *cmdEnv) requireOutbox(want ...string) {
	e.t.Helper()
	got, err := e.env.OutboxKeys(e.ctx)
	e.must(err)
	if !slices.Equal(got, want) {
		e.t.Fatalf("outbox keys = %v; want %v", got, want)
	}
}

func entry(repository, comment int64, d commands.Disposition) commands.Entry {
	e := commands.Entry{RepositoryID: repository, CommentID: comment, PullNumber: 1, AuthorID: 7, CreatedAt: cmdTime(1),
		BodyDigest: artifact.Hash([]byte("body")), Disposition: d, DecidedAt: cmdTime(9)}
	if d == commands.Processed {
		e.Operation = "consent-operation"
	}
	return e
}

func ack(key string) *governance.OutboxMessage {
	return &governance.OutboxMessage{Key: key, Kind: "pr_comment", Payload: json.RawMessage(`{"operation":"x"}`)}
}

// RunConformance runs the commands.Store conformance suite against the
// environments made by newEnv.
func RunConformance(t *testing.T, newEnv func(*testing.T) Env) {
	run := func(name string, test func(*cmdEnv)) {
		t.Run(name, func(t *testing.T) {
			env := newEnv(t)
			test(&cmdEnv{t: t, ctx: t.Context(), env: env, store: env.Store})
		})
	}
	first := func(id int64) observed {
		return observed{pull: 1, id: id, created: 1, updated: 1, body: "/suiteward approve P1-R1 123456"}
	}

	run("Pending returns only comments of enrolled repositories", func(e *cmdEnv) {
		e.observe(10, 5, first(1))
		e.requirePending(100, 10) // nobody is enrolled yet
		e.enroll(20)
		e.requirePending(100, 10) // repository 10 is not enrolled
		e.enroll(10, 20)
		e.requirePending(100, 10, candidate(10, 5, first(1)))
		e.enroll(20) // a re-enrollment replaces the projects
		e.requirePending(100, 10)
	})

	run("Pending is ordered by creation time then comment id, bounded by the limit and the horizon", func(e *cmdEnv) {
		e.enroll(10, 20)
		c := func(id int64, created int) observed {
			return observed{pull: 1, id: id, created: created, updated: created, body: "c"}
		}
		e.observe(10, 6, c(5, 3), c(9, 1), c(11, 5))
		e.observe(20, 6, c(3, 3), c(7, 2))
		inOrder := []commands.Candidate{candidate(10, 6, c(9, 1)), candidate(20, 6, c(7, 2)), candidate(20, 6, c(3, 3)), candidate(10, 6, c(5, 3))}
		e.requirePending(5, 10, inOrder...) // created exactly at the horizon is excluded
		e.requirePending(5, 3, inOrder[:3]...)
		e.requirePending(6, 10, append(slices.Clone(inOrder), candidate(10, 6, c(11, 5)))...)
		e.requirePending(1, 10)
		for _, limit := range []int{0, -1} {
			_, err := e.store.Pending(e.ctx, cmdTime(5), limit)
			e.requireErr(err, commands.ErrInvalid)
		}
		_, err := e.store.Pending(e.ctx, time.Time{}, 10)
		e.requireErr(err, commands.ErrInvalid)
	})

	run("Pending returns only the first observed version of a comment", func(e *cmdEnv) {
		e.enroll(10)
		original := observed{pull: 1, id: 1, created: 1, updated: 1, body: "/suiteward approve P1-R1 123456"}
		e.observe(10, 5, original)
		e.observe(10, 6, observed{pull: 1, id: 1, created: 1, updated: 4, body: "edited once"})
		e.observe(10, 7, observed{pull: 1, id: 1, created: 1, updated: 6, body: "edited twice"})
		e.requirePending(100, 10, candidate(10, 5, original))

		// A comment first observed already edited is returned as first observed: that is how it is recognized.
		editedFirst := observed{pull: 2, id: 2, created: 2, updated: 3, body: "edited before observed"}
		e.observe(10, 8, original, editedFirst)
		e.observe(10, 9, original, observed{pull: 2, id: 2, created: 2, updated: 8, body: "edited again"})
		e.requirePending(100, 10, candidate(10, 5, original), candidate(10, 8, editedFirst))
	})

	run("Pending leaves out the comments that have a ledger entry", func(e *cmdEnv) {
		e.enroll(10, 20)
		e.observe(10, 5, first(1), first(2))
		e.observe(20, 5, first(3))
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.NotCommand), nil))
		e.requirePending(100, 10, candidate(10, 5, first(2)), candidate(20, 5, first(3)))
		e.must(e.store.Record(e.ctx, entry(20, 3, commands.Processed), nil))
		e.requirePending(100, 10, candidate(10, 5, first(2)))
		e.must(e.store.Record(e.ctx, entry(10, 2, commands.Malformed), ack("ack-2")))
		e.requirePending(100, 10)
		// An edit of a recorded comment is not a new candidate: only the first observed version is processed, once.
		e.observe(10, 6, observed{pull: 1, id: 1, created: 1, updated: 5, body: "edited"}, first(2))
		e.requirePending(100, 10)
	})

	run("Record stores every disposition", func(e *cmdEnv) {
		all := []commands.Disposition{commands.NotCommand, commands.BeforeEnrollment, commands.NoSuite, commands.EditedFirst, commands.Failed,
			commands.IgnoredAuthor, commands.Malformed, commands.InvalidCode, commands.ReplayedCode, commands.Processed}
		for i, d := range all {
			e.must(e.store.Record(e.ctx, entry(10, int64(i+1), d), nil))
			e.must(e.store.Record(e.ctx, entry(10, int64(i+1), d), nil)) // stored: a repeat is the same entry
			other := entry(10, int64(i+1), d)
			other.DecidedAt = cmdTime(10)
			e.requireErr(e.store.Record(e.ctx, other, nil), commands.ErrConflict)
		}
		e.requireOutbox() // no acknowledgment was given
	})

	run("Record is idempotent and writes the acknowledgment once", func(e *cmdEnv) {
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.Malformed), ack("ack-1")))
		e.requireOutbox("ack-1")
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.Malformed), ack("ack-1")))
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.Malformed), ack("another")))
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.Malformed), nil))
		e.requireOutbox("ack-1")
		e.must(e.store.Record(e.ctx, entry(10, 2, commands.Processed), nil))
		e.must(e.store.Record(e.ctx, entry(10, 2, commands.Processed), nil))
		e.requireOutbox("ack-1")
	})

	run("a conflicting entry is rejected and writes nothing", func(e *cmdEnv) {
		original := entry(10, 1, commands.Malformed)
		e.must(e.store.Record(e.ctx, original, ack("ack-1")))
		differs := map[string]func(*commands.Entry){
			"disposition": func(x *commands.Entry) { x.Disposition = commands.InvalidCode },
			"processed":   func(x *commands.Entry) { x.Disposition, x.Operation = commands.Processed, "op" },
			"pull":        func(x *commands.Entry) { x.PullNumber = 2 },
			"author":      func(x *commands.Entry) { x.AuthorID = 8 },
			"created":     func(x *commands.Entry) { x.CreatedAt = cmdTime(2) },
			"digest":      func(x *commands.Entry) { x.BodyDigest = artifact.Hash([]byte("other")) },
			"decided":     func(x *commands.Entry) { x.DecidedAt = cmdTime(10) },
		}
		for name, mutate := range differs {
			other := original
			mutate(&other)
			e.t.Run(name, func(t *testing.T) {
				sub := *e
				sub.t = t
				sub.requireErr(sub.store.Record(sub.ctx, other, ack("ack-other-"+name)), commands.ErrConflict)
			})
		}
		e.requireOutbox("ack-1") // no conflicting attempt wrote its acknowledgment
		e.must(e.store.Record(e.ctx, original, nil))

		// The key is the (repository, comment) pair: the same comment id elsewhere is another comment.
		e.must(e.store.Record(e.ctx, entry(20, 1, commands.InvalidCode), nil))
	})

	run("an invalid entry is not recorded", func(e *cmdEnv) {
		e.enroll(10)
		e.observe(10, 5, first(1))
		bad := map[string]func(*commands.Entry){
			"repository":              func(x *commands.Entry) { x.RepositoryID = 0 },
			"comment":                 func(x *commands.Entry) { x.CommentID = 0 },
			"author":                  func(x *commands.Entry) { x.AuthorID = 0 },
			"pull":                    func(x *commands.Entry) { x.PullNumber = 0 },
			"huge pull":               func(x *commands.Entry) { x.PullNumber = 1 << 31 },
			"creation time":           func(x *commands.Entry) { x.CreatedAt = time.Time{} },
			"decision time":           func(x *commands.Entry) { x.DecidedAt = time.Time{} },
			"digest":                  func(x *commands.Entry) { x.BodyDigest = artifact.Digest{} },
			"unknown disposition":     func(x *commands.Entry) { x.Disposition = "approved" },
			"empty disposition":       func(x *commands.Entry) { x.Disposition = "" },
			"processed no operation":  func(x *commands.Entry) { x.Disposition, x.Operation = commands.Processed, "" },
			"operation not processed": func(x *commands.Entry) { x.Operation = "op" },
		}
		for name, mutate := range bad {
			x := entry(10, 1, commands.NotCommand)
			mutate(&x)
			e.t.Run(name, func(t *testing.T) {
				sub := *e
				sub.t = t
				sub.requireErr(sub.store.Record(sub.ctx, x, ack("ack-bad")), commands.ErrInvalid)
			})
		}
		e.requireOutbox()
		e.requirePending(100, 10, candidate(10, 5, first(1)))
	})

	run("a failing acknowledgment rolls the entry back", func(e *cmdEnv) {
		e.enroll(10)
		e.observe(10, 5, first(1), first(2), first(3))
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.Malformed), ack("ack-1")))

		// The key is taken: the second entry is not recorded either.
		e.requireErr(e.store.Record(e.ctx, entry(10, 2, commands.InvalidCode), ack("ack-1")), governance.ErrOperationConflict)
		// The message is invalid.
		e.requireErr(e.store.Record(e.ctx, entry(10, 2, commands.InvalidCode), &governance.OutboxMessage{Key: "ack-2", Kind: "Bad", Payload: json.RawMessage(`{}`)}),
			governance.ErrInvalidRequest)
		e.requireOutbox("ack-1")
		e.requirePending(100, 10, candidate(10, 5, first(2)), candidate(10, 5, first(3)))

		// Nothing was left behind: the comment is recorded once its acknowledgment is good.
		e.must(e.store.Record(e.ctx, entry(10, 2, commands.InvalidCode), ack("ack-2")))
		e.requireOutbox("ack-1", "ack-2")
	})

	run("concurrent Record of one comment: exactly one entry wins", func(e *cmdEnv) {
		const callers = 8
		results := make([]error, callers)
		var ready, done sync.WaitGroup
		ready.Add(callers)
		start := make(chan struct{})
		for i := range callers {
			done.Add(1)
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				// Even callers record one entry, odd callers another, each with its own acknowledgment.
				d := commands.Malformed
				if i%2 == 1 {
					d = commands.InvalidCode
				}
				results[i] = e.store.Record(e.ctx, entry(10, 1, d), ack("ack-"+string(d)))
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		wins, conflicts := 0, 0
		for _, err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, commands.ErrConflict):
				conflicts++
			default:
				e.t.Fatalf("Record = %v; want success or ErrConflict", err)
			}
		}
		if wins != callers/2 || conflicts != callers/2 {
			e.t.Fatalf("%d callers succeeded and %d conflicted; want %d of each", wins, conflicts, callers/2)
		}
		if keys, err := e.env.OutboxKeys(e.ctx); err != nil || len(keys) != 1 {
			e.t.Fatalf("outbox keys = %v, error %v; want the winner's acknowledgment only", keys, err)
		}
	})

	run("Instance is a stable 128-bit hexadecimal identifier", func(e *cmdEnv) {
		id, err := e.store.Instance(e.ctx)
		e.must(err)
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
			e.t.Fatalf("Instance = %q; want 32 lowercase hexadecimal digits", id)
		}
		again, err := e.store.Instance(e.ctx)
		e.must(err)
		if again != id {
			e.t.Fatalf("Instance changed from %q to %q", id, again)
		}
	})

	run("a canceled context writes nothing", func(e *cmdEnv) {
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		_, err := e.store.Pending(ctx, cmdTime(5), 10)
		e.requireErr(err, context.Canceled)
		e.requireErr(e.store.Record(ctx, entry(10, 1, commands.Malformed), ack("ack-1")), context.Canceled)
		_, err = e.store.Instance(ctx)
		e.requireErr(err, context.Canceled)
		e.requireOutbox()
		e.must(e.store.Record(e.ctx, entry(10, 1, commands.NotCommand), nil)) // the canceled attempt left no entry
	})
}
