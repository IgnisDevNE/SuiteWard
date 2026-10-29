package executiontest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// ledgerEntry is a ledger entry of repository 10, pull 1, created at the time created.
func ledgerEntry(comment int64, d commands.Disposition, created time.Duration) commands.Entry {
	e := commands.Entry{RepositoryID: 10, CommentID: comment, PullNumber: 1, AuthorID: 7, CreatedAt: at(created), BodyDigest: artifact.Hash([]byte("body")),
		Disposition: d, DecidedAt: at(created + time.Second)}
	if d == commands.Processed {
		e.Operation = "consent-operation"
	}
	return e
}

func ack(key string) *governance.OutboxMessage {
	return &governance.OutboxMessage{Key: key, Kind: "pr_comment", Payload: json.RawMessage(`{"operation":"x"}`)}
}

func (e *env) requireUnblocked(id int64, since time.Duration, want bool) {
	e.t.Helper()
	got, err := e.s.Unblocked(e.ctx, id, at(since))
	e.must(err)
	if got != want {
		e.t.Fatalf("Unblocked(%d, since %v) = %v; want %v", id, since, got, want)
	}
}

// requireEntry requires the ledger to hold exactly this entry for its comment.
func (e *env) requireEntry(want commands.Entry) {
	e.t.Helper()
	e.must(e.env.Ledger.Record(e.ctx, want, nil)) // the same entry again is success
	other := want
	other.Disposition, other.Operation, other.Mode = commands.Malformed, "", ""
	e.requireErr(e.env.Ledger.Record(e.ctx, other, nil), commands.ErrConflict)
}

// requireNoEntry requires that the ledger has no entry for the comment, and leaves a malformed one there.
func (e *env) requireNoEntry(repository, comment int64) {
	e.t.Helper()
	x := ledgerEntry(comment, commands.Malformed, 0)
	x.RepositoryID = repository
	e.must(e.env.Ledger.Record(e.ctx, x, nil))
}

func (e *env) requireUnblocks(comment int64, want ...int64) {
	e.t.Helper()
	got, err := e.env.Unblocks(e.ctx, 10, comment)
	e.must(err)
	if !slices.Equal(got, want) {
		e.t.Fatalf("unblock rows of comment %d = jobs %v; want %v", comment, got, want)
	}
}

func apApproval(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	gate := execution.ApprovalGate{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunComplete, JobID: job.ID, Summary: execution.SummaryMet}
	requireGate := func(comment int64, want execution.ApprovalGate) {
		e.t.Helper()
		got, found, err := e.env.Gate(e.ctx, 10, comment)
		e.must(err)
		if !found || got != want {
			e.t.Fatalf("approval receipt of comment %d = %+v, found %v; want %+v", comment, got, found, want)
		}
	}
	processed := ledgerEntry(1, commands.Processed, 0)
	processed.Mode = "fix"
	e.must(e.s.RecordApproval(e.ctx, processed, nil, gate))
	e.requireEntry(processed)
	requireGate(1, gate)

	// A refusal carries its acknowledgment; every state of the run is stored as it is.
	refusal := ledgerEntry(2, commands.ReceiptRefused, time.Minute)
	pendingGate := execution.ApprovalGate{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelBeforeApproval, RunState: execution.RunPending, JobID: job.ID}
	e.must(e.s.RecordApproval(e.ctx, refusal, ack("ack-2"), pendingGate))
	e.requireEntry(refusal)
	requireGate(2, pendingGate)
	e.requireOutbox("ack-2")
	gates := []execution.ApprovalGate{
		{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelAdvisory, RunState: execution.RunNone},
		{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunNotApplicable},
		{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunUnavailable, JobID: job.ID, Unblocked: true},
		{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunComplete, JobID: job.ID, Summary: execution.SummaryNotMet},
		{HeadSHA: sha(1), ReceiptLevel: domain.ReceiptLevelRequired, RunState: execution.RunComplete, JobID: job.ID},
	}
	for i, g := range gates {
		comment := int64(10 + i)
		e.must(e.s.RecordApproval(e.ctx, ledgerEntry(comment, commands.ReceiptRefused, 0), nil, g))
		requireGate(comment, g)
	}

	// The same approval again is success and writes nothing more; another gate or entry for the comment conflicts.
	e.must(e.s.RecordApproval(e.ctx, refusal, ack("ack-2"), pendingGate))
	e.requireErr(e.s.RecordApproval(e.ctx, refusal, nil, gate), commands.ErrConflict)
	differs := refusal
	differs.DecidedAt = differs.DecidedAt.Add(time.Minute)
	e.requireErr(e.s.RecordApproval(e.ctx, differs, nil, pendingGate), commands.ErrConflict)
	requireGate(2, pendingGate)
	e.requireOutbox("ack-2")

	// A refused record writes nothing: no entry, no acknowledgment and no row.
	with := func(mutate func(*execution.ApprovalGate)) execution.ApprovalGate {
		g := gate
		mutate(&g)
		return g
	}
	badGates := map[string]execution.ApprovalGate{
		"none with a job":            with(func(g *execution.ApprovalGate) { g.RunState = execution.RunNone }),
		"not-applicable with a job":  with(func(g *execution.ApprovalGate) { g.RunState = execution.RunNotApplicable }),
		"pending without a job":      with(func(g *execution.ApprovalGate) { g.RunState, g.JobID, g.Summary = execution.RunPending, 0, "" }),
		"complete without a job":     with(func(g *execution.ApprovalGate) { g.JobID = 0 }),
		"unavailable without a job":  with(func(g *execution.ApprovalGate) { g.RunState, g.JobID, g.Summary = execution.RunUnavailable, 0, "" }),
		"an unknown job":             with(func(g *execution.ApprovalGate) { g.JobID = 987654 }),
		"a receipt level of off":     with(func(g *execution.ApprovalGate) { g.ReceiptLevel = domain.ReceiptLevelOff }),
		"no receipt level":           with(func(g *execution.ApprovalGate) { g.ReceiptLevel = 0 }),
		"an unknown run state":       with(func(g *execution.ApprovalGate) { g.RunState = "running" }),
		"a summary on a pending run": with(func(g *execution.ApprovalGate) { g.RunState, g.Summary = execution.RunPending, execution.SummaryMet }),
		"an unknown summary":         with(func(g *execution.ApprovalGate) { g.Summary = "maybe" }),
		"a malformed head":           with(func(g *execution.ApprovalGate) { g.HeadSHA = "abc" }),
	}
	next := int64(200)
	for name, g := range badGates {
		next++
		comment := next
		e.t.Run(name, func(t *testing.T) {
			sub := *e
			sub.t = t
			sub.requireErr(sub.s.RecordApproval(sub.ctx, ledgerEntry(comment, commands.Processed, 0), ack("ack-bad"), g), execution.ErrInvalidRequest)
			if _, found, err := sub.env.Gate(sub.ctx, 10, comment); err != nil || found {
				t.Fatalf("approval receipt of comment %d found %v, error %v; want none", comment, found, err)
			}
			sub.requireNoEntry(10, comment)
		})
	}
	for name, d := range map[string]commands.Disposition{"a malformed command": commands.Malformed, "an unblock": commands.Unblocked, "a mode": commands.ModeSet} {
		next++
		entry := ledgerEntry(next, d, 0)
		entry.Mode = "fix"
		e.requireErr(e.s.RecordApproval(e.ctx, entry, ack("ack-bad"), gate), execution.ErrInvalidRequest)
		if _, found, err := e.env.Gate(e.ctx, 10, next); err != nil || found {
			e.t.Fatalf("%s: approval receipt found %v, error %v; want none", name, found, err)
		}
		e.requireNoEntry(10, next)
	}
	invalid := ledgerEntry(500, commands.Processed, 0)
	invalid.Operation = ""
	e.requireErr(e.s.RecordApproval(e.ctx, invalid, nil, gate), commands.ErrInvalid)
	e.requireOutbox("ack-2")
}

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cooldownMinutes = 15 // checks.RepublishCooldown in minutes, written out so that the suite pins the contract's number
)

func rcKey(repository int64, pull int, generation int64) string {
	return fmt.Sprintf("receipt-check:%d:%d:%d", repository, pull, generation)
}

// rcMessage builds the outbox message of a generation the way the job does.
func rcMessage(repository int64, pull int) func(int64) (governance.OutboxMessage, error) {
	return func(generation int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: rcKey(repository, pull, generation), Kind: "github_receipt_check", Payload: json.RawMessage(fmt.Sprintf(`{"generation":%d}`, generation))}, nil
	}
}

func rcResult(head string, state execution.CheckState, reason string) execution.ReceiptCheck {
	return execution.ReceiptCheck{RepositoryID: 10, PullNumber: 1, HeadSHA: head, State: state, Reason: reason, JobID: 5}
}

// publish publishes r for its pull with the standard message, minutes after the origin, and requires it to succeed.
func (e *env) publish(r execution.ReceiptCheck, minutes int) (execution.ReceiptRecord, bool) {
	e.t.Helper()
	record, written, err := e.env.Receipts.Publish(e.ctx, r, at(time.Duration(minutes)*time.Minute), rcMessage(r.RepositoryID, r.PullNumber))
	e.must(err)
	return record, written
}

func (e *env) requirePublished(got execution.ReceiptRecord, written, wantWritten bool, generation int64) {
	e.t.Helper()
	if written != wantWritten || got.Generation != generation {
		e.t.Fatalf("Publish = generation %d, message written %v; want generation %d, written %v", got.Generation, written, generation, wantWritten)
	}
}

// requireReceipt requires the current record of pull 1 of repository 10.
func (e *env) requireReceipt(want execution.ReceiptRecord) {
	e.t.Helper()
	got, found, err := e.env.Receipts.Current(e.ctx, 10, 1)
	e.must(err)
	if !found || diff(got, want, "record") != "" {
		e.t.Fatalf("Current = %+v, found %v;\nwant %+v", got, found, want)
	}
}

func (e *env) requireNoReceipt(repository int64, pull int) {
	e.t.Helper()
	if got, found, err := e.env.Receipts.Current(e.ctx, repository, pull); err != nil || found {
		e.t.Fatalf("Current(%d, %d) = %+v, found %v, error %v; want no record", repository, pull, got, found, err)
	}
}

func rcRules(e *env) {
	e.requireNoReceipt(10, 1)
	first := rcResult(headA, execution.CheckInProgress, "receipt-running")
	first.Reference, first.JobID = "P1-R2", 0
	record, written := e.publish(first, 1)
	e.requirePublished(record, written, true, 1)
	want := execution.ReceiptRecord{ReceiptCheck: first, HeadSince: at(time.Minute), Generation: 1, DecidedAt: at(time.Minute)}
	if diff := diff(record, want, "record"); diff != "" {
		e.t.Fatal(diff)
	}
	e.requireReceipt(want)
	e.requireOutbox(rcKey(10, 1, 1))

	// A result that changes none of the five identity fields moves the job id and the decision time only.
	again := first
	again.JobID = 9
	record, written = e.publish(again, 5)
	e.requirePublished(record, written, false, 1)
	e.requireReceipt(execution.ReceiptRecord{ReceiptCheck: again, HeadSince: at(time.Minute), Generation: 1, DecidedAt: at(5 * time.Minute)})
	e.requireOutbox(rcKey(10, 1, 1))

	// A new head, state (neutral included), reason, reference or unblocked advances the generation by exactly one.
	current, since := again, at(time.Minute) // only a new head restarts it
	for i, step := range []struct {
		name   string
		mutate func(*execution.ReceiptCheck)
	}{
		{"head", func(r *execution.ReceiptCheck) { r.HeadSHA = headB }},
		{"state", func(r *execution.ReceiptCheck) { r.State = execution.CheckNeutral }},
		{"reason", func(r *execution.ReceiptCheck) { r.Reason = "receipt-not-run" }},
		{"reference", func(r *execution.ReceiptCheck) { r.Reference = "P1-R3" }},
		{"unblocked", func(r *execution.ReceiptCheck) { r.Unblocked = true }},
		{"state again", func(r *execution.ReceiptCheck) { r.State = execution.CheckFailure }},
	} {
		minute := 6 + i
		step.mutate(&current)
		if step.name == "head" {
			since = at(time.Duration(minute) * time.Minute)
		}
		record, written := e.publish(current, minute)
		e.requirePublished(record, written, true, int64(i+2))
		if !record.HeadSince.Equal(since) || !record.DecidedAt.Equal(at(time.Duration(minute)*time.Minute)) || record.Unblocked != current.Unblocked {
			e.t.Fatalf("after a new %s: head since %v, decided at %v, unblocked %v; want %v, %v, %v", step.name, record.HeadSince, record.DecidedAt, record.Unblocked,
				since, at(time.Duration(minute)*time.Minute), current.Unblocked)
		}
	}
	e.requireOutbox(rcKey(10, 1, 1), rcKey(10, 1, 2), rcKey(10, 1, 3), rcKey(10, 1, 4), rcKey(10, 1, 5), rcKey(10, 1, 6), rcKey(10, 1, 7))

	// A message that ended failed is sent again under the next generation a whole cooldown after it failed, and not before.
	e.must(e.env.FailMessage(e.ctx, rcKey(10, 1, 7), at(20*time.Minute)))
	record, written = e.publish(current, 20+cooldownMinutes-1)
	e.requirePublished(record, written, false, 7)
	record, written = e.publish(current, 20+cooldownMinutes)
	e.requirePublished(record, written, true, 8)
	record, written = e.publish(current, 20+cooldownMinutes+1) // the new message is pending: quiet again
	e.requirePublished(record, written, false, 8)
	// A changed result is not a retry: it publishes inside the cooldown.
	e.must(e.env.FailMessage(e.ctx, rcKey(10, 1, 8), at(40*time.Minute)))
	changed := current
	changed.State = execution.CheckSuccess
	record, written = e.publish(changed, 41)
	e.requirePublished(record, written, true, 9)

	// Pulls and repositories are independent.
	other := rcResult(headB, execution.CheckSuccess, "receipt-complete")
	other.PullNumber = 2
	e.publish(other, 50)
	elsewhere := rcResult(headB, execution.CheckNeutral, "receipt-not-run")
	elsewhere.RepositoryID = 20
	e.publish(elsewhere, 51)
	for _, c := range []struct {
		repository int64
		pull       int
		state      execution.CheckState
	}{{10, 2, execution.CheckSuccess}, {20, 1, execution.CheckNeutral}} {
		got, found, err := e.env.Receipts.Current(e.ctx, c.repository, c.pull)
		e.must(err)
		if !found || got.State != c.state || got.Generation != 1 {
			e.t.Fatalf("Current(%d, %d) = %+v, found %v; want state %s, generation 1", c.repository, c.pull, got, found, c.state)
		}
	}
	e.requireNoReceipt(20, 2)
}

func rcConcurrent(e *env) {
	const callers = 8
	written := make([]bool, callers)
	failures := make([]error, callers)
	parallel(callers, func(i int) {
		_, written[i], failures[i] = e.env.Receipts.Publish(e.ctx, rcResult(headA, execution.CheckInProgress, "receipt-running"), at(time.Minute), rcMessage(10, 1))
	})
	messages := 0
	for i, err := range failures {
		e.must(err)
		if written[i] {
			messages++
		}
	}
	if messages != 1 {
		e.t.Fatalf("%d callers wrote a message; want exactly one", messages)
	}
	e.requireOutbox(rcKey(10, 1, 1))

	// Results that all differ from the stored one advance the generation once each.
	reasons := make([]bool, callers)
	parallel(callers, func(i int) {
		_, reasons[i], failures[i] = e.env.Receipts.Publish(e.ctx, rcResult(headA, execution.CheckInProgress, fmt.Sprintf("reason-%d", i)), at(2*time.Minute), rcMessage(10, 1))
	})
	for i, err := range failures {
		e.must(err)
		if !reasons[i] {
			e.t.Fatalf("caller %d wrote no message although its result differs from the stored one", i)
		}
	}
	current, found, err := e.env.Receipts.Current(e.ctx, 10, 1)
	e.must(err)
	keys, err := e.env.OutboxKeys(e.ctx)
	e.must(err)
	if !found || int(current.Generation) != callers+1 || len(keys) != callers+1 {
		e.t.Fatalf("generation %d with outbox keys %v; want %d of each", current.Generation, keys, callers+1)
	}
}

func rcInvalid(e *env) {
	requireInvalid(e, func() execution.ReceiptCheck { return rcResult(headA, execution.CheckSuccess, "receipt-complete") }, map[string]func(*execution.ReceiptCheck){
		"no repository":      func(r *execution.ReceiptCheck) { r.RepositoryID = 0 },
		"no pull":            func(r *execution.ReceiptCheck) { r.PullNumber = 0 },
		"a pull past int32":  func(r *execution.ReceiptCheck) { r.PullNumber = 1 << 31 },
		"a short head":       func(r *execution.ReceiptCheck) { r.HeadSHA = "abc123" },
		"an upper-case head": func(r *execution.ReceiptCheck) { r.HeadSHA = strings.ToUpper(headA) },
		"no head":            func(r *execution.ReceiptCheck) { r.HeadSHA = "" },
		"no reason":          func(r *execution.ReceiptCheck) { r.Reason = "" },
		"a blank reason":     func(r *execution.ReceiptCheck) { r.Reason = " \t" },
		"a state of skipped": func(r *execution.ReceiptCheck) { r.State = "skipped" },
		"no state":           func(r *execution.ReceiptCheck) { r.State = "" },
		"a reference":        func(r *execution.ReceiptCheck) { r.Reference = "P1" },
		"a reference text":   func(r *execution.ReceiptCheck) { r.Reference = "P1-R2 " },
		"a negative job":     func(r *execution.ReceiptCheck) { r.JobID = -1 },
	}, func(r execution.ReceiptCheck) error {
		_, _, err := e.env.Receipts.Publish(e.ctx, r, at(time.Minute), rcMessage(10, 1))
		return err
	})
	valid := rcResult(headA, execution.CheckSuccess, "receipt-complete")
	_, _, err := e.env.Receipts.Publish(e.ctx, valid, time.Time{}, rcMessage(10, 1))
	e.requireErr(err, execution.ErrInvalidRequest)
	for _, c := range []struct {
		repository int64
		pull       int
	}{{0, 1}, {10, 0}} {
		_, _, err := e.env.Receipts.Current(e.ctx, c.repository, c.pull)
		e.requireErr(err, execution.ErrInvalidRequest)
	}
	e.requireNoReceipt(10, 1)
	e.requireOutbox()

	// A message that cannot be built, is invalid or has a key that is taken leaves the record as it was.
	boom := errors.New("cannot build the message")
	failing := func(int64) (governance.OutboxMessage, error) { return governance.OutboxMessage{}, boom }
	_, _, err = e.env.Receipts.Publish(e.ctx, valid, at(time.Minute), failing)
	e.requireErr(err, boom)
	badMessage := func(int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: rcKey(10, 1, 1), Kind: "Bad", Payload: json.RawMessage(`{}`)}, nil
	}
	_, _, err = e.env.Receipts.Publish(e.ctx, valid, at(time.Minute), badMessage)
	e.requireErr(err, governance.ErrInvalidRequest)
	e.requireNoReceipt(10, 1)
	e.publish(valid, 2)
	before := execution.ReceiptRecord{ReceiptCheck: valid, HeadSince: at(2 * time.Minute), Generation: 1, DecidedAt: at(2 * time.Minute)}
	next := valid
	next.State = execution.CheckFailure
	_, _, err = e.env.Receipts.Publish(e.ctx, next, at(3*time.Minute), failing)
	e.requireErr(err, boom)
	e.requireReceipt(before)
	taken := rcResult(headA, execution.CheckSuccess, "receipt-complete")
	taken.RepositoryID = 20
	_, _, err = e.env.Receipts.Publish(e.ctx, taken, at(3*time.Minute), rcMessage(10, 1)) // key receipt-check:10:1:1 exists
	e.requireErr(err, governance.ErrOperationConflict)
	e.requireNoReceipt(20, 1)
	// The retry of a lost publication that fails changes nothing either, and a quiet update never builds a message.
	e.must(e.env.FailMessage(e.ctx, rcKey(10, 1, 1), at(3*time.Minute)))
	_, _, err = e.env.Receipts.Publish(e.ctx, valid, at((3+cooldownMinutes)*time.Minute), failing)
	e.requireErr(err, boom)
	e.requireReceipt(before)
	e.publish(valid, 4+cooldownMinutes) // the retry succeeds: generation 2
	quiet := valid
	quiet.JobID = 8
	record, written, err := e.env.Receipts.Publish(e.ctx, quiet, at((5+cooldownMinutes)*time.Minute), failing)
	e.must(err)
	e.requirePublished(record, written, false, 2)

	// A time keeps the microseconds that PostgreSQL stores, and a canceled context writes nothing.
	precise := rcResult(headB, execution.CheckSuccess, "receipt-complete")
	precise.PullNumber = 3
	record, _, err = e.env.Receipts.Publish(e.ctx, precise, at(time.Hour).Add(789*time.Nanosecond), rcMessage(10, 3))
	e.must(err)
	if !record.HeadSince.Equal(at(time.Hour)) || !record.DecidedAt.Equal(at(time.Hour)) {
		e.t.Fatalf("times = %v, %v; want %v", record.HeadSince, record.DecidedAt, at(time.Hour))
	}
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	precise.PullNumber = 4
	_, _, err = e.env.Receipts.Publish(ctx, precise, at(time.Hour), rcMessage(10, 4))
	e.requireErr(err, context.Canceled)
	_, _, err = e.env.Receipts.Current(ctx, 10, 3)
	e.requireErr(err, context.Canceled)
	e.requireNoReceipt(10, 4)
}
