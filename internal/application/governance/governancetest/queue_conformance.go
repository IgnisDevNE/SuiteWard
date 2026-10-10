package governancetest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// QueueInspector is implemented by stores that can list what committed units
// of work enqueued, which the port itself cannot read. Subtests that need it
// are skipped for stores that do not implement it.
type QueueInspector interface {
	QueuedJobKinds(ctx context.Context) ([]string, error) // the kind of every committed job, sorted
	OutboxKeys(ctx context.Context) ([]string, error)     // the key of every committed outbox message, sorted
}

var cfObject = json.RawMessage(`{"suite":"suite"}`)

// requireQueued fails unless exactly the given job kinds and outbox keys are committed.
func (e *cfEnv) requireQueued(kinds, keys []string) {
	e.t.Helper()
	inspector, ok := e.store.(QueueInspector)
	if !ok {
		e.t.Skip("the store cannot list its queued work")
	}
	gotKinds, err := inspector.QueuedJobKinds(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	gotKeys, err := inspector.OutboxKeys(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	if !slices.Equal(gotKinds, kinds) || !slices.Equal(gotKeys, keys) {
		e.t.Fatalf("queued jobs %v and outbox keys %v, want %v and %v", gotKinds, gotKeys, kinds, keys)
	}
}

func cfEnqueueAndOutbox(ctx context.Context, tx governance.Tx, key string) error {
	if err := tx.Enqueue(ctx, governance.Job{Kind: "probe", Args: cfObject}); err != nil {
		return err
	}
	return tx.Outbox(ctx, governance.OutboxMessage{Key: key, Kind: "probe", Payload: cfObject})
}

func cfQueueCommitsWithTheFact(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	before := e.revision()
	approve := e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		write, err := e.consentWrite(ctx, tx, approve)
		if err != nil {
			return err
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return err
		}
		return cfEnqueueAndOutbox(ctx, tx, "publish-p1")
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireQueued([]string{"probe"}, []string{"publish-p1"})
	e.requireWrites(before, 1) // the consent only: the queue does not advance the revision
}

func cfQueueRollsBackWithTheFact(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1"}, sources: []contract.SourceCommandID{"comment-p1"}}
	before := e.view(probe)
	approve := e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		write, err := e.consentWrite(ctx, tx, approve)
		if err != nil {
			return err
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return err
		}
		if err := cfEnqueueAndOutbox(ctx, tx, "publish-p1"); err != nil {
			return err
		}
		return errCfAbort
	})
	if !errors.Is(err, errCfAbort) {
		e.t.Fatalf("error = %v, want the error of the unit of work", err)
	}
	e.requireSameView(before, e.view(probe))
	e.requireQueued(nil, nil)

	// The key of the failed attempt is free again.
	if err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfEnqueueAndOutbox(ctx, tx, "publish-p1") }); err != nil {
		e.t.Fatalf("retry after rollback = %v", err)
	}
	e.requireQueued([]string{"probe"}, []string{"publish-p1"})
}

func cfQueueAloneCommits(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	before := e.revision()

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		return cfEnqueueAndOutbox(ctx, tx, "alone")
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireQueued([]string{"probe"}, []string{"alone"})
	e.requireWrites(before, 0)
}

func cfInvalidQueueWritesAreRejected(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	oversized := json.RawMessage(`{"a":"` + strings.Repeat("x", 64<<10) + `"}`)
	job := func(j governance.Job) func(context.Context, governance.Tx) error {
		return func(ctx context.Context, tx governance.Tx) error { return tx.Enqueue(ctx, j) }
	}
	message := func(m governance.OutboxMessage) func(context.Context, governance.Tx) error {
		return func(ctx context.Context, tx governance.Tx) error { return tx.Outbox(ctx, m) }
	}
	for name, write := range map[string]func(context.Context, governance.Tx) error{
		"job with a bad kind":         job(governance.Job{Kind: "Bad", Args: cfObject}),
		"job with array args":         job(governance.Job{Kind: "probe", Args: json.RawMessage(`[]`)}),
		"job with oversized args":     job(governance.Job{Kind: "probe", Args: oversized}),
		"message without a key":       message(governance.OutboxMessage{Kind: "probe", Payload: cfObject}),
		"message with a bad kind":     message(governance.OutboxMessage{Key: "k", Kind: "Bad", Payload: cfObject}),
		"message with a non-object":   message(governance.OutboxMessage{Key: "k", Kind: "probe", Payload: json.RawMessage(`1`)}),
		"message with a huge payload": message(governance.OutboxMessage{Key: "k", Kind: "probe", Payload: oversized}),
	} {
		e.run(name, func(e *cfEnv) {
			err := e.do(func(ctx context.Context, tx governance.Tx) error {
				if err := tx.Enqueue(ctx, governance.Job{Kind: "probe", Args: cfObject}); err != nil {
					return err
				}
				return write(ctx, tx)
			})
			if !errors.Is(err, governance.ErrInvalidRequest) {
				e.t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			e.requireQueued(nil, nil) // the valid job of the same unit of work is rolled back too
		})
	}
}

func cfDuplicateOutboxKeyConflicts(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	if err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfEnqueueAndOutbox(ctx, tx, "dup") }); err != nil {
		e.t.Fatal(err)
	}

	err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfEnqueueAndOutbox(ctx, tx, "dup") })
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("repeated key = %v, want ErrOperationConflict", err)
	}
	e.requireQueued([]string{"probe"}, []string{"dup"}) // the job of the conflicting unit of work is not kept

	err = e.do(func(ctx context.Context, tx governance.Tx) error {
		if err := tx.Outbox(ctx, governance.OutboxMessage{Key: "twice", Kind: "probe", Payload: cfObject}); err != nil {
			return err
		}
		return tx.Outbox(ctx, governance.OutboxMessage{Key: "twice", Kind: "probe", Payload: cfObject})
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("key repeated inside one unit of work = %v, want ErrOperationConflict", err)
	}
	e.requireQueued([]string{"probe"}, []string{"dup"})
}

func cfQueueWriteFailureRollsBack(e *cfEnv) {
	fail, ok := e.store.(FailNexter)
	if !ok {
		e.t.Skip("the store cannot inject a write failure")
	}
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	boom := errors.New("conformance: injected queue failure")

	fail.FailNext(boom)
	err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfEnqueueAndOutbox(ctx, tx, "failing") })
	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the injected failure", err)
	}
	e.requireQueued(nil, nil)

	fail.FailNext(boom)
	err = e.do(func(ctx context.Context, tx governance.Tx) error {
		if err := tx.Outbox(ctx, governance.OutboxMessage{Key: "failing", Kind: "probe", Payload: cfObject}); err != nil {
			return err
		}
		return tx.Enqueue(ctx, governance.Job{Kind: "probe", Args: cfObject})
	})
	if !errors.Is(err, boom) {
		e.t.Fatalf("outbox error = %v, want the injected failure", err)
	}
	e.requireQueued(nil, nil)
}

func cfQueueIsNotCommittedOnCancellation(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := e.store.Do(ctx, cfProject, cfSuite, func(ctx context.Context, tx governance.Tx) error {
		if err := cfEnqueueAndOutbox(ctx, tx, "canceled"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		e.t.Fatal("Do committed queued work after its context was canceled")
	}
	e.requireQueued(nil, nil)
}

func cfQueueIsUnusableAfterTheUnitOfWork(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	var leaked governance.Tx
	e.read(func(_ context.Context, tx governance.Tx) error {
		leaked = tx
		return nil
	})
	ctx := context.Background()
	if err := leaked.Enqueue(ctx, governance.Job{Kind: "probe", Args: cfObject}); err == nil {
		e.t.Error("a job can still be enqueued after the unit of work ended")
	}
	if err := leaked.Outbox(ctx, governance.OutboxMessage{Key: "late", Kind: "probe", Payload: cfObject}); err == nil {
		e.t.Error("an outbox message can still be written after the unit of work ended")
	}
	e.requireQueued(nil, nil)
}
