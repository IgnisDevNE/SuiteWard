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

// requireRefusedWrite fails the subtest unless write is refused with the sentinel want and leaves the probed view as it was.
func (e *cfEnv) requireRefusedWrite(probe cfProbe, before cfView, want error, write func(context.Context, governance.Tx) error) {
	e.t.Helper()
	if err := e.do(write); !errors.Is(err, want) {
		e.t.Fatalf("error = %v, want %v", err, want)
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
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"guard-op", "guard-op-2", "guard-op-3", "guard-op-4"}, sources: []contract.SourceCommandID{"guard-src", "guard-src-2", "guard-src-3"}}
	before := e.view(probe)
	resultOf := func(command contract.Command) contract.CommandResult {
		return cfMust(contract.ReconstituteCommandResult(command, contract.ConsentApproved, contract.ConsentReasonNone))
	}
	writeOf := func(command contract.Command) governance.ConsentWrite {
		result := resultOf(command)
		return governance.ConsentWrite{OperationID: command.OperationID(), Result: result,
			Receipt: governance.ConsentReceipt{Result: result, EvaluatedReference: command.Reference(), PolicyRevisionID: cfPolicy, CurrentApprovalEligible: true}}
	}
	// replayed, below, is the result of the same command applied twice: a duplicate, which only an alias write may record.
	apply := func(consent contract.Consent) (contract.Consent, contract.CommandResult) {
		next, result, err := consent.Apply(c.proposal, e.policy(cfProject), approve)
		if err != nil {
			panic(err)
		}
		return next, result
	}
	approved, _ := apply(cfMust(contract.NewConsent(cfProject, cfSuite, c.proposalID())))
	_, replayed := apply(approved)

	otherSource := writeOf(approve)
	otherSource.Receipt.Result = resultOf(another)
	noOperation := writeOf(approve)
	noOperation.OperationID = ""
	noCommand := writeOf(approve)
	noCommand.Result = contract.CommandResult{}
	duplicateWithoutAlias := writeOf(approve)
	duplicateWithoutAlias.Result = replayed
	aliasOfANewResult := writeOf(approve)
	aliasOfANewResult.Alias = true
	otherOperation := writeOf(approve)
	otherOperation.OperationID = "guard-op-4"
	for _, tc := range []struct {
		name  string
		write governance.ConsentWrite
		want  error
	}{
		{"a command of another suite", writeOf(foreign), governance.ErrInvalidRequest},
		{"a receipt of another source command", otherSource, governance.ErrInvalidRequest},
		{"no operation id", noOperation, governance.ErrInvalidRequest},
		{"a result without a command", noCommand, governance.ErrInvalidRequest},
		{"a duplicate result that is not an alias", duplicateWithoutAlias, governance.ErrOperationConflict},
		{"an alias of a result that is not a duplicate", aliasOfANewResult, governance.ErrOperationConflict},
		{"an operation id other than the command's", otherOperation, governance.ErrOperationConflict},
	} {
		e.run(tc.name, func(e *cfEnv) {
			e.requireRefusedWrite(probe, before, tc.want, func(ctx context.Context, tx governance.Tx) error { return tx.AppendConsent(ctx, tc.write) })
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
	noRecord := write()
	noRecord.Receipt.Record = contract.PromotionRecord{}
	noVersion := write()
	noVersion.Version = contract.SuiteVersion{}
	versionOfAnotherSuite := write() // its record names this suite, its version another
	versionOfAnotherSuite.Version = cfMust(contract.NewSuiteVersion(cfProject, "other-suite", "version-3", r.open.protected.Manifest()))
	for _, tc := range []struct {
		name  string
		write governance.PromotionWrite
	}{
		{"a promotion of another suite", e.promotionWrite(other, "guard-op", "version-3", "")},
		{"a receipt request that names another operation", otherOperation},
		{"a receipt identity that corrects another version", otherCorrection},
		{"a receipt identity bound to another revision", otherBinding},
		{"a record time that PostgreSQL cannot keep", subMicrosecond},
		{"no promotion record", noRecord},
		{"no version id", noVersion},
		{"a version of another suite under a record of this one", versionOfAnotherSuite},
	} {
		e.run(tc.name, func(e *cfEnv) {
			e.requireRefusedWrite(probe, before, governance.ErrInvalidRequest, func(ctx context.Context, tx governance.Tx) error { return tx.RecordPromotion(ctx, tc.write) })
		})
	}
}
