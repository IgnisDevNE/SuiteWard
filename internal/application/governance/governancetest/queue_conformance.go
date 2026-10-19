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

// OutboxInspector is implemented by stores that can list what committed units of work wrote to the outbox, which the port
// itself cannot read. Subtests that need it are skipped for stores that do not implement it.
type OutboxInspector interface {
	OutboxKeys(ctx context.Context) ([]string, error) // the key of every committed outbox message, sorted
}

var cfObject = json.RawMessage(`{"suite":"suite"}`)

// requireOutbox fails unless exactly the given outbox keys are committed.
func (e *cfEnv) requireOutbox(keys []string) {
	e.t.Helper()
	inspector, ok := e.store.(OutboxInspector)
	if !ok {
		e.t.Skip("the store cannot list its outbox")
	}
	gotKeys, err := inspector.OutboxKeys(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	if !slices.Equal(gotKeys, keys) {
		e.t.Fatalf("outbox keys %v, want %v", gotKeys, keys)
	}
}

func cfWriteOutbox(ctx context.Context, tx governance.Tx, key string) error {
	return tx.Outbox(ctx, governance.OutboxMessage{Key: key, Kind: "probe", Payload: cfObject})
}

func cfOutboxCommitsWithTheFact(e *cfEnv) {
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
		return cfWriteOutbox(ctx, tx, "publish-p1")
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireOutbox([]string{"publish-p1"})
	e.requireWrites(before, 1) // the consent only: the outbox does not advance the revision
}

func cfOutboxRollsBackWithTheFact(e *cfEnv) {
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
		if err := cfWriteOutbox(ctx, tx, "publish-p1"); err != nil {
			return err
		}
		return errCfAbort
	})
	if !errors.Is(err, errCfAbort) {
		e.t.Fatalf("error = %v, want the error of the unit of work", err)
	}
	e.requireSameView(before, e.view(probe))
	e.requireOutbox(nil)

	// The key of the failed attempt is free again.
	if err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfWriteOutbox(ctx, tx, "publish-p1") }); err != nil {
		e.t.Fatalf("retry after rollback = %v", err)
	}
	e.requireOutbox([]string{"publish-p1"})
}

func cfOutboxAloneCommits(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	before := e.revision()

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		return cfWriteOutbox(ctx, tx, "alone")
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireOutbox([]string{"alone"})
	e.requireWrites(before, 0)
}

func cfInvalidOutboxWritesAreRejected(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	oversized := json.RawMessage(`{"a":"` + strings.Repeat("x", 64<<10) + `"}`)
	for name, message := range map[string]governance.OutboxMessage{
		"message without a key":       {Kind: "probe", Payload: cfObject},
		"message with a bad kind":     {Key: "k", Kind: "Bad", Payload: cfObject},
		"message with a non-object":   {Key: "k", Kind: "probe", Payload: json.RawMessage(`1`)},
		"message with a huge payload": {Key: "k", Kind: "probe", Payload: oversized},
	} {
		e.run(name, func(e *cfEnv) {
			err := e.do(func(ctx context.Context, tx governance.Tx) error {
				if err := cfWriteOutbox(ctx, tx, "valid"); err != nil {
					return err
				}
				return tx.Outbox(ctx, message)
			})
			if !errors.Is(err, governance.ErrInvalidRequest) {
				e.t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			e.requireOutbox(nil) // the valid message of the same unit of work is rolled back too
		})
	}
}

func cfDuplicateOutboxKeyConflicts(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	if err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfWriteOutbox(ctx, tx, "dup") }); err != nil {
		e.t.Fatal(err)
	}

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		if err := cfWriteOutbox(ctx, tx, "other"); err != nil {
			return err
		}
		return cfWriteOutbox(ctx, tx, "dup")
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("repeated key = %v, want ErrOperationConflict", err)
	}
	e.requireOutbox([]string{"dup"}) // the message of the conflicting unit of work is not kept

	err = e.do(func(ctx context.Context, tx governance.Tx) error {
		if err := cfWriteOutbox(ctx, tx, "twice"); err != nil {
			return err
		}
		return cfWriteOutbox(ctx, tx, "twice")
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("key repeated inside one unit of work = %v, want ErrOperationConflict", err)
	}
	e.requireOutbox([]string{"dup"})
}

func cfOutboxWriteFailureRollsBack(e *cfEnv) {
	fail, ok := e.store.(FailNexter)
	if !ok {
		e.t.Skip("the store cannot inject a write failure")
	}
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	boom := errors.New("conformance: injected outbox failure")

	fail.FailNext(boom)
	err := e.do(func(ctx context.Context, tx governance.Tx) error { return cfWriteOutbox(ctx, tx, "failing") })
	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the injected failure", err)
	}
	e.requireOutbox(nil)
}

func cfOutboxIsNotCommittedOnCancellation(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := e.store.Do(ctx, cfProject, cfSuite, func(ctx context.Context, tx governance.Tx) error {
		if err := cfWriteOutbox(ctx, tx, "canceled"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		e.t.Fatal("Do committed an outbox message after its context was canceled")
	}
	e.requireOutbox(nil)
}

func cfOutboxIsUnusableAfterTheUnitOfWork(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("p1", "", "v1"))
	var leaked governance.Tx
	e.read(func(_ context.Context, tx governance.Tx) error {
		leaked = tx
		return nil
	})
	if err := cfWriteOutbox(context.Background(), leaked, "late"); err == nil {
		e.t.Error("an outbox message can still be written after the unit of work ended")
	}
	e.requireOutbox(nil)
}
