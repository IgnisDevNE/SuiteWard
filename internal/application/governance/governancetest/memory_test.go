package governancetest

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// These tests pin behavior of the in-memory store that the shared conformance
// suite cannot require of every store.

func memoryEnv(t *testing.T) *cfEnv {
	t.Helper()
	return &cfEnv{t: t, store: NewMemory(), owner: cfMust(contract.NewPrincipal("owner", contract.Human))}
}

func TestFailNextIsConsumedByTheNextUnitOfWork(t *testing.T) {
	e := memoryEnv(t)
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	boom := errors.New("injected")

	e.store.(FailNexter).FailNext(boom)
	e.read(func(context.Context, governance.Tx) error { return nil }) // a unit of work without a write

	if response := e.approve(c); !response.Committed {
		t.Fatalf("response = %+v, want a failure armed for an earlier unit of work not to hit this one", response)
	}
}

func TestAliasWriteRejectsAReceiptOtherThanTheOriginal(t *testing.T) {
	e := memoryEnv(t)
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	original := e.approve(c)
	retry := e.command(c, "revision-1", "approve-retry", "comment-p1", contract.ApproveConsent, 1)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		write, err := e.consentWrite(ctx, tx, retry)
		if err != nil {
			return err
		}
		write.Receipt.CurrentApprovalEligible = !original.Receipt.CurrentApprovalEligible
		return tx.AppendConsent(ctx, write)
	})

	if !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("error = %v, want an invalid request for an alias that does not carry the original receipt", err)
	}
}
