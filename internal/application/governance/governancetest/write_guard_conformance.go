package governancetest

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The writes a store must refuse as invalid before it stores anything. PostgreSQL always refused them; Memory accepted
// some, so a test that ran on Memory could pass on a write production would reject. Both stores now call the Validate
// of the write, and these cases hold them to it: each row breaks one thing.

func writeGuardCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"InvalidConsentWritesAreRefusedAndWriteNothing", cfInvalidConsentWrites},
		{"InvalidPromotionWritesAreRefusedAndWriteNothing", cfInvalidPromotionWrites},
	}
}

// requireInvalidWrite fails the subtest unless write is refused as an invalid request and leaves the probed view as it was.
func (e *cfEnv) requireInvalidWrite(probe cfProbe, before cfView, write func(context.Context, governance.Tx) error) {
	e.t.Helper()
	if err := e.do(write); !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("error = %v, want an invalid request", err)
	}
	e.requireSameView(before, e.view(probe))
}

func cfInvalidConsentWrites(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	other := e.seedOtherSuite()
	approve := e.command(c, "revision-1", "guard-op", "guard-src", contract.ApproveConsent, 1)
	another := e.command(c, "revision-1", "guard-op-2", "guard-src-2", contract.ApproveConsent, 2)
	foreign := e.command(other, "revision-1", "guard-op-3", "guard-src-3", contract.ApproveConsent, 1)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"guard-op", "guard-op-2", "guard-op-3"}, sources: []contract.SourceCommandID{"guard-src", "guard-src-2", "guard-src-3"}}
	before := e.view(probe)
	resultOf := func(command contract.Command) contract.CommandResult {
		return cfMust(contract.ReconstituteCommandResult(command, contract.ConsentApproved, contract.ConsentReasonNone))
	}
	writeOf := func(command contract.Command) governance.ConsentWrite {
		result := resultOf(command)
		return governance.ConsentWrite{OperationID: command.OperationID(), Result: result,
			Receipt: governance.ConsentReceipt{Result: result, EvaluatedReference: command.Reference(), PolicyRevisionID: cfPolicy, CurrentApprovalEligible: true}}
	}
	otherSource := writeOf(approve)
	otherSource.Receipt.Result = resultOf(another)
	noOperation := writeOf(approve)
	noOperation.OperationID = ""
	for _, tc := range []struct {
		name  string
		write governance.ConsentWrite
	}{
		{"a command of another suite", writeOf(foreign)},
		{"a receipt of another source command", otherSource},
		{"no operation id", noOperation},
	} {
		e.run(tc.name, func(e *cfEnv) {
			e.requireInvalidWrite(probe, before, func(ctx context.Context, tx governance.Tx) error { return tx.AppendConsent(ctx, tc.write) })
		})
	}
}

func cfInvalidPromotionWrites(e *cfEnv) {
	r := e.seedRich()
	other := e.seedOtherSuite()
	probe := cfProbe{proposal: "p2", operations: []contract.OperationID{"guard-op", "other-op"}, versions: []contract.SuiteVersionID{"version-3"}, references: []contract.ProposalReference{r.open.current()}}
	before := e.view(probe)
	write := func() governance.PromotionWrite { return e.promotionWrite(r.open, "guard-op", "version-3", "") }
	otherOperation := write()
	otherOperation.Receipt.Identity.Request.OperationID = "other-op"
	otherCorrection := write()
	otherCorrection.Receipt.Identity.CorrectsVersionID = "version-1"
	otherBinding := write()
	otherBinding.Receipt.Identity.Binding = r.open.bindings["revision-1"]
	subMicrosecond := write()
	record := subMicrosecond.Receipt.Record
	subMicrosecond.Receipt.Record = cfMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: record.OperationID(), VersionID: record.VersionID(), Binding: record.Binding(),
		Carrier: record.Carrier(), Source: record.Source(), Target: record.Target(), RecordedAt: record.RecordedAt().Add(time.Nanosecond)}))
	for _, tc := range []struct {
		name  string
		write governance.PromotionWrite
	}{
		{"a promotion of another suite", e.promotionWrite(other, "guard-op", "version-3", "")},
		{"a receipt request that names another operation", otherOperation},
		{"a receipt identity that corrects another version", otherCorrection},
		{"a receipt identity bound to another revision", otherBinding},
		{"a record time that PostgreSQL cannot keep", subMicrosecond},
	} {
		e.run(tc.name, func(e *cfEnv) {
			e.requireInvalidWrite(probe, before, func(ctx context.Context, tx governance.Tx) error { return tx.RecordPromotion(ctx, tc.write) })
		})
	}
}
