//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var errInjected = errors.New("injected failure")

func TestNewStoreChecksSchemaReadiness(t *testing.T) {
	t.Run("unmigrated schema", func(t *testing.T) {
		database := newSchemaDatabase(t)
		pool, err := pgxpool.New(t.Context(), database.url)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		content, err := filesystem.NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := postgres.NewStore(pool, content, testInserter()); !errors.Is(err, postgres.ErrSchemaNotReady) {
			t.Fatalf("NewStore on an unmigrated schema = %v; want ErrSchemaNotReady", err)
		}
	})
	t.Run("another schema version", func(t *testing.T) {
		w := newPGWorld(t)
		if _, err := w.db.conn.Exec(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (11, true)"); err != nil {
			t.Fatal(err)
		}
		if _, err := postgres.NewStore(w.pool, w.content, testInserter()); !errors.Is(err, postgres.ErrSchemaNotReady) {
			t.Fatalf("NewStore on schema version 11 = %v; want ErrSchemaNotReady", err)
		}
	})
	t.Run("an older schema version", func(t *testing.T) {
		database := newSchemaDatabase(t)
		if err := migrations.UpTo(t.Context(), database.url, migrations.SupportedVersion-1); err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.New(t.Context(), database.url)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		content, err := filesystem.NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := postgres.NewStore(pool, content, testInserter()); !errors.Is(err, postgres.ErrSchemaNotReady) {
			t.Fatalf("NewStore on the version before SupportedVersion = %v; want ErrSchemaNotReady", err)
		}
	})
	t.Run("missing dependencies", func(t *testing.T) {
		w := newPGWorld(t)
		if _, err := postgres.NewStore(nil, w.content, testInserter()); err == nil {
			t.Fatal("NewStore accepted a nil pool")
		}
		if _, err := postgres.NewStore(w.pool, nil, testInserter()); err == nil {
			t.Fatal("NewStore accepted a nil artifact verifier")
		}
		if _, err := postgres.NewStore(w.pool, w.content, nil); err == nil {
			t.Fatal("NewStore accepted a nil job inserter")
		}
	})
}

func TestDoRejectsAnUnknownSuite(t *testing.T) {
	w := newPGWorld(t)
	called := false
	err := w.store.Do(t.Context(), fxProject, "missing", func(context.Context, governance.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, governance.ErrNotFound) || called {
		t.Fatalf("Do on an unknown Suite = %v (callback ran: %v); want ErrNotFound without running it", err, called)
	}
}

// consentWrite builds the write a consent use case would issue for an approval.
func (w *pgWorld) consentWrite(c candidate, operation, source string, order contract.CommandOrder) governance.ConsentWrite {
	w.t.Helper()
	command := w.command(c, c.current().RevisionID, operation, source, contract.ApproveConsent, order)
	consent := must(contract.NewConsent(c.current().ProjectID, c.current().SuiteID, contract.ProposalID(c.id)))
	policy := must(contract.NewPolicy(fxProject, fxPolicy, w.owner))
	_, result, err := consent.Apply(c.proposal, policy, command)
	if err != nil {
		w.t.Fatal(err)
	}
	return governance.ConsentWrite{OperationID: command.OperationID(), Result: result,
		Receipt: governance.ConsentReceipt{Result: result, EvaluatedReference: c.current(), PolicyRevisionID: fxPolicy, CurrentApprovalEligible: true}}
}

func TestDoCommitsOnlyWhenTheCallbackSucceeds(t *testing.T) {
	assertUntouched := func(t *testing.T, w *pgWorld) {
		t.Helper()
		if n := w.count("consent_results") + w.count("operations"); n != 0 || w.revision() != 7 {
			t.Fatalf("%d consent rows and revision %d remain after an uncommitted unit of work; want none and 7", n, w.revision())
		}
	}
	setup := func(t *testing.T) (*pgWorld, candidate) {
		w := newPGWorld(t)
		c := newCandidate("p1", "", w.protected("a"))
		w.seedSuite(fxProject, fxSuite, c)
		return w, c
	}
	t.Run("commit", func(t *testing.T) {
		w, c := setup(t)
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			return tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1))
		})
		if err != nil || w.count("consent_results") != 1 || w.revision() != 8 {
			t.Fatalf("commit: error %v, %d rows, revision %d; want 1 row at revision 8", err, w.count("consent_results"), w.revision())
		}
	})
	t.Run("error", func(t *testing.T) {
		w, c := setup(t)
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			if err := tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1)); err != nil {
				return err
			}
			return errInjected
		})
		if !errors.Is(err, errInjected) {
			t.Fatalf("Do = %v; want the callback error", err)
		}
		assertUntouched(t, w)
	})
	t.Run("panic", func(t *testing.T) {
		w, c := setup(t)
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("the panic was swallowed")
				}
			}()
			_ = w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
				if err := tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1)); err != nil {
					return err
				}
				panic("boom")
			})
		}()
		assertUntouched(t, w) // also proves the lock and connection were released
	})
	t.Run("canceled before start", func(t *testing.T) {
		w, _ := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		called := false
		err := w.store.Do(ctx, fxProject, fxSuite, func(context.Context, governance.Tx) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("Do with a canceled context = %v (callback ran: %v); want context.Canceled without running it", err, called)
		}
	})
	t.Run("canceled during the callback", func(t *testing.T) {
		w, c := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		err := w.store.Do(ctx, fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			if err := tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1)); err != nil {
				return err
			}
			cancel()
			return nil // the callback itself succeeds, but the context is gone
		})
		if err == nil {
			t.Fatal("Do committed after its context was canceled")
		}
		assertUntouched(t, w)
	})
}

func TestSeedRoundTripsEveryStoredFact(t *testing.T) {
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("base"))
	revised := newCandidate("p1", "v0", w.protected("one"), "r1", "r2")
	// Assessments with missing evidence, evidence for another source, and evidence
	// that cites the earlier revision of the same proposal.
	r1, r2 := revised.proposal.Revisions()[0].Binding(), revised.proposal.Revisions()[1].Binding()
	missing := must(contract.AssessIntegrity("pending", r2, nil))
	otherSource := must(contract.NewIntegrityEvidence("verifier", "elsewhere", r2, contract.IntegrityPassed))
	wrongSource := must(contract.AssessIntegrity("pending-2", r2, &otherSource))
	failed := must(contract.NewIntegrityEvidence("verifier", "failing", r2, contract.IntegrityFailed))
	failing := must(contract.AssessIntegrity("failing", r2, &failed))
	unavailable := must(contract.NewIntegrityEvidence("verifier", "offline", r2, contract.IntegrityUnavailable))
	offline := must(contract.AssessIntegrity("offline", r2, &unavailable))
	earlier := must(contract.NewIntegrityEvidence("verifier", "stale", r1, contract.IntegrityPassed))
	stale := must(contract.AssessIntegrity("stale", r2, &earlier))
	revised.assessments = append(revised.assessments, missing, wrongSource, failing, offline, stale)
	w.seedWithBaseline(base, revised)

	w.read(func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current, present := state.Canonical.Suite().CurrentVersionID()
		if !present || current != "v0" || state.Canonical.Suite().Revision() != 3 || state.Target != fxTarget ||
			state.Policy.RevisionID() != fxPolicy || state.Policy.OwnerID() != "owner" || state.Policy.ProjectID() != fxProject {
			t.Fatalf("suite state %+v", state)
		}
		if !state.Canonical.Contract().Equal(base.protected) || state.Canonical.Record().OperationID() != "seed-promote" || state.Canonical.Record().Source() != base.merged ||
			!state.Canonical.Record().RecordedAt().Equal(fxRecordedAt) || !state.Canonical.Record().Binding().Equal(base.proposal.Current().Binding()) {
			t.Fatalf("canonical %+v", state.Canonical)
		}
		proposal, consent, err := tx.Proposal(ctx, "p1")
		if err != nil {
			t.Fatal(err)
		}
		stored := proposal.Revisions()
		if len(stored) != 2 || !stored[0].Binding().Equal(r1) || !stored[1].Binding().Equal(r2) || stored[1].Origin() != revised.origin || stored[1].Carrier() != revised.carrier || len(consent.Results()) != 0 {
			t.Fatalf("proposal revisions %d, results %d", len(stored), len(consent.Results()))
		}
		if _, _, err := tx.Proposal(ctx, "unknown"); !errors.Is(err, governance.ErrNotFound) {
			t.Fatalf("unknown proposal = %v; want ErrNotFound", err)
		}
		for _, want := range append([]contract.IntegrityAssessment{}, revised.assessments...) {
			got, found, err := tx.Assessment(ctx, want.Binding().Reference(), want.Source())
			if err != nil || !found || got.Reason() != want.Reason() || got.Passed() != want.Passed() || !got.Binding().Equal(want.Binding()) || got.Source() != want.Source() {
				t.Fatalf("assessment %q of %s: %+v found=%v err=%v; want reason %v", want.Source(), want.Binding().Reference().RevisionID, got, found, err, want.Reason())
			}
			wantEvidence, wantHas := want.Evidence()
			gotEvidence, gotHas := got.Evidence()
			if wantHas != gotHas || (wantHas && (gotEvidence.EmitterID() != wantEvidence.EmitterID() || gotEvidence.Source() != wantEvidence.Source() ||
				gotEvidence.Outcome() != wantEvidence.Outcome() || !gotEvidence.Binding().Equal(wantEvidence.Binding()))) {
				t.Fatalf("assessment %q evidence %+v; want %+v", want.Source(), gotEvidence, wantEvidence)
			}
		}
		if _, found, err := tx.Assessment(ctx, revised.current(), "never-assessed"); err != nil || found {
			t.Fatalf("unknown assessment found=%v err=%v", found, err)
		}
		other := revised.current()
		other.SuiteID = "another-suite"
		if _, found, err := tx.Assessment(ctx, other, revised.merged); err != nil || found {
			t.Fatalf("assessment in another Suite found=%v err=%v", found, err)
		}
		historical, found, err := tx.Version(ctx, "v0")
		if err != nil || !found || historical.Version().Manifest().Digest() != base.protected.Manifest().Digest() || historical.Record().VersionID() != "v0" {
			t.Fatalf("version v0 %+v found=%v err=%v", historical, found, err)
		}
		if _, found, err := tx.Version(ctx, "v9"); err != nil || found {
			t.Fatalf("unknown version found=%v err=%v", found, err)
		}
		record, found, err := tx.PromotionFor(ctx, base.current())
		if err != nil || !found || record.VersionID() != "v0" || record.Carrier() != base.carrier || record.Target() != fxTarget || record.CorrectsVersionID() != "" {
			t.Fatalf("promotion for the baseline %+v found=%v err=%v", record, found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, revised.current()); err != nil || found {
			t.Fatalf("promotion for an unpromoted proposal found=%v err=%v", found, err)
		}
		return nil
	})
	var outcomes []string
	rows, err := w.db.conn.Query(t.Context(), "SELECT coalesce(outcome, 'none') FROM assessments WHERE source IN ('pending', 'pending-2', 'failing', 'offline') ORDER BY source")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var outcome string
		if err := rows.Scan(&outcome); err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, outcome)
	}
	rows.Close()
	if strings.Join(outcomes, ",") != "failed,unavailable,none,passed" {
		t.Fatalf("stored assessment outcomes %v", outcomes)
	}
}

func TestSeedRejectsInconsistentState(t *testing.T) {
	w := newPGWorld(t)
	one := newCandidate("p1", "", w.protected("one"))
	two := newCandidate("p2", "", w.protected("two"))
	seedWith := func(extra contract.IntegrityAssessment) governance.Seed {
		seed := w.emptySeed(fxProject, fxSuite, one, two)
		seed.Proposals[0].Assessments = append(seed.Proposals[0].Assessments, extra)
		return seed
	}
	ghost := one.current()
	ghost.RevisionID = "ghost"
	ghostBinding := must(contract.NewApprovalBinding(contract.BindingInput{Reference: ghost, Manifest: one.protected.Manifest().Digest(), Scope: one.protected.ScopeDigest(),
		PolicyRevision: fxPolicy, CoveredInputs: one.protected.CoveredInputs()}))
	foreignEvidence := must(contract.NewIntegrityEvidence("verifier", "src", two.proposal.Current().Binding(), contract.IntegrityPassed))
	unknownEvidence := must(contract.NewIntegrityEvidence("verifier", "src", ghostBinding, contract.IntegrityPassed))
	tamperedBinding := must(contract.NewApprovalBinding(contract.BindingInput{Reference: one.current(), Manifest: one.protected.Manifest().Digest(), Scope: one.protected.ScopeDigest(),
		PolicyRevision: fxPolicy, CoveredInputs: map[string]string{"runner": "other"}}))
	tamperedEvidence := must(contract.NewIntegrityEvidence("verifier", "src", tamperedBinding, contract.IntegrityPassed))
	missingHistory := w.emptySeed(fxProject, fxSuite, one)
	version := must(contract.NewSuiteVersion(fxProject, fxSuite, "v0", one.protected.Manifest()))
	record := must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "op", VersionID: "v0", Binding: one.proposal.Current().Binding(), Carrier: one.carrier,
		Source: one.merged, Target: fxTarget, RecordedAt: fxRecordedAt}))
	missingHistory.Suite.Canonical = must(contract.NewCanonicalSnapshot(must(contract.NewSuite(fxProject, fxSuite, "v0", 1)), version, one.protected, record))
	cases := []struct {
		name string
		seed governance.Seed
	}{
		{"evidence bound to another proposal", seedWith(must(contract.AssessIntegrity("src", one.proposal.Current().Binding(), &foreignEvidence)))},
		{"evidence bound to no stored revision", seedWith(must(contract.AssessIntegrity("src", one.proposal.Current().Binding(), &unknownEvidence)))},
		{"evidence bound to different covered inputs", seedWith(must(contract.AssessIntegrity("src", one.proposal.Current().Binding(), &tamperedEvidence)))},
		{"assessment of a revision that was not seeded", seedWith(must(contract.AssessIntegrity("src", ghostBinding, nil)))},
		{"current version missing from history", missingHistory},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := w.store.Seed(t.Context(), tt.seed); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("Seed = %v; want ErrInvalidRequest", err)
			}
			for _, table := range []string{"policies", "suites", "proposals", "proposal_revisions", "assessments", "suite_versions", "promotions"} {
				if n := w.count(table); n != 0 {
					t.Fatalf("%d rows remain in %s after a rejected seed", n, table)
				}
			}
		})
	}
	t.Run("the same Suite twice", func(t *testing.T) {
		w.seedSuite(fxProject, fxSuite, one)
		before := w.count("proposals")
		if err := w.store.Seed(t.Context(), w.emptySeed(fxProject, fxSuite, one)); !errors.Is(err, governance.ErrInvalidRequest) || w.count("proposals") != before {
			t.Fatalf("second Seed = %v; want ErrInvalidRequest without changes", err)
		}
	})
	t.Run("a policy revision with another owner", func(t *testing.T) {
		impostor := must(contract.NewPrincipal("impostor", contract.Human))
		seed := w.emptySeed(fxProject, "suite-b", newCandidateIn(fxProject, "suite-b", "p1", "", one.protected))
		seed.Suite.Policy = must(contract.NewPolicy(fxProject, fxPolicy, impostor))
		if err := w.store.Seed(t.Context(), seed); !errors.Is(err, governance.ErrInvalidRequest) {
			t.Fatalf("Seed with a conflicting policy revision = %v; want ErrInvalidRequest", err)
		}
	})
	t.Run("an identical policy revision is shared", func(t *testing.T) {
		w.seedSuite(fxProject, "suite-c", newCandidateIn(fxProject, "suite-c", "p1", "", one.protected))
	})
}

func TestConsentWritesFollowTheStoreSemantics(t *testing.T) {
	w := newPGWorld(t)
	c := newCandidate("p1", "", w.protected("a"))
	w.seedSuite(fxProject, fxSuite, c)
	agent := must(contract.NewPrincipal("agent", contract.Agent))

	response := w.consent(w.command(c, c.current().RevisionID, "op-approve", "src-approve", contract.ApproveConsent, 1))
	if response.Duplicate || !response.Receipt.CurrentApprovalEligible || w.revision() != 8 {
		t.Fatalf("approval %+v at revision %d; want a new eligible result at revision 8", response, w.revision())
	}
	var action, outcome, reason, actorKind, operation string
	var order int64
	if err := w.db.conn.QueryRow(t.Context(), "SELECT action, outcome, reason, actor_kind, operation_id, command_order FROM consent_results WHERE source_command_id = 'src-approve'").
		Scan(&action, &outcome, &reason, &actorKind, &operation, &order); err != nil || action != "approve" || outcome != "approved" || reason != "none" || actorKind != "human" || operation != "op-approve" || order != 1 {
		t.Fatalf("stored result %q %q %q %q %q %d, error %v", action, outcome, reason, actorKind, operation, order, err)
	}
	var kind, receiptKind string
	if err := w.db.conn.QueryRow(t.Context(), "SELECT kind, receipt->>'kind' FROM operations WHERE operation_id = 'op-approve'").Scan(&kind, &receiptKind); err != nil || kind != "consent" || receiptKind != "consent" {
		t.Fatalf("stored operation kind %q receipt kind %q, error %v", kind, receiptKind, err)
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		byOperation, found, err := tx.Receipt(ctx, "op-approve")
		if err != nil || !found || byOperation.Kind != governance.OperationConsent || byOperation.ProjectID != fxProject || byOperation.SuiteID != fxSuite || byOperation.Consent == nil || byOperation.Promotion != nil {
			t.Fatalf("receipt %+v found=%v err=%v", byOperation, found, err)
		}
		stored := byOperation.Consent
		command := stored.Result.Command()
		if stored.Result.Outcome() != contract.ConsentApproved || stored.Result.Duplicate() || command.OperationID() != "op-approve" || command.SourceCommandID() != "src-approve" ||
			command.Actor() != w.owner || command.Reference() != c.current() || command.Carrier() != c.carrier || command.Action() != contract.ApproveConsent || command.Order() != 1 ||
			stored.EvaluatedReference != c.current() || stored.PolicyRevisionID != fxPolicy || !stored.CurrentApprovalEligible || stored.PromotedVersionID != "" {
			t.Fatalf("stored consent receipt %+v", stored)
		}
		bySource, found, err := tx.ReceiptBySource(ctx, "src-approve")
		if err != nil || !found || bySource.Consent == nil || bySource.Consent.Result.Command().OperationID() != "op-approve" {
			t.Fatalf("receipt by source %+v found=%v err=%v", bySource, found, err)
		}
		if _, found, err := tx.Receipt(ctx, "unknown"); err != nil || found {
			t.Fatalf("unknown receipt found=%v err=%v", found, err)
		}
		return nil
	})

	t.Run("replay of the same operation changes nothing", func(t *testing.T) {
		replay := w.consent(w.command(c, c.current().RevisionID, "op-approve", "src-approve", contract.ApproveConsent, 1))
		if !replay.Duplicate || w.revision() != 8 {
			t.Fatalf("replay %+v at revision %d; want a duplicate at revision 8", replay, w.revision())
		}
	})
	t.Run("a new operation for a processed source is an alias that advances the revision", func(t *testing.T) {
		alias := w.consent(w.command(c, c.current().RevisionID, "op-alias", "src-approve", contract.ApproveConsent, 1))
		if !alias.Duplicate || alias.Receipt.Result.Command().OperationID() != "op-approve" || w.revision() != 9 {
			t.Fatalf("alias %+v at revision %d; want the original receipt at revision 9", alias, w.revision())
		}
		if n := w.count("consent_results"); n != 1 {
			t.Fatalf("%d consent results; an alias must not add one", n)
		}
		var source string
		if err := w.db.conn.QueryRow(t.Context(), "SELECT source_command_id FROM operations WHERE operation_id = 'op-alias'").Scan(&source); err != nil || source != "src-approve" {
			t.Fatalf("alias operation points at %q, error %v; want the original source command", source, err)
		}
		w.read(func(ctx context.Context, tx governance.Tx) error {
			receipt, found, err := tx.Receipt(ctx, "op-alias")
			if err != nil || !found || receipt.Consent == nil || receipt.Consent.Result.Command().OperationID() != "op-approve" {
				t.Fatalf("alias receipt %+v found=%v err=%v; want a copy of the original", receipt, found, err)
			}
			original, _, err := tx.ReceiptBySource(ctx, "src-approve")
			if err != nil || original.Consent.Result.Command().OperationID() != "op-approve" {
				t.Fatalf("receipt by source %+v err=%v; want the original operation, not the alias", original, err)
			}
			proposal, consent, err := tx.Proposal(ctx, "p1")
			if err != nil {
				t.Fatal(err)
			}
			_, result, err := consent.Apply(proposal, must(contract.NewPolicy(fxProject, fxPolicy, w.owner)), w.command(c, c.current().RevisionID, "op-alias", "src-approve", contract.ApproveConsent, 1))
			if err != nil || !result.Duplicate() {
				t.Fatalf("applying the alias after reconstitution: duplicate=%v err=%v; want a duplicate", result.Duplicate(), err)
			}
			return nil
		})
		again := w.consent(w.command(c, c.current().RevisionID, "op-alias", "src-approve", contract.ApproveConsent, 1))
		if !again.Duplicate || w.revision() != 9 {
			t.Fatalf("replay of the alias %+v at revision %d; want a duplicate at revision 9", again, w.revision())
		}
	})
	t.Run("a source command replayed by another actor conflicts", func(t *testing.T) {
		stolen := must(contract.NewCommand(contract.CommandInput{OperationID: "op-thief", SourceCommandID: "src-approve", Actor: agent, Reference: c.current(),
			Carrier: c.carrier, Action: contract.ApproveConsent, Order: 1}))
		_, err := governance.ProcessConsent(t.Context(), w.store, governance.ConsentRequest{Command: stolen})
		if !errors.Is(err, governance.ErrOperationConflict) || w.revision() != 9 || w.count("operations") != 2 {
			t.Fatalf("source replayed by another actor = %v at revision %d; want ErrOperationConflict and no write", err, w.revision())
		}
	})
	t.Run("a rejected command is stored with its stable reason", func(t *testing.T) {
		unauthorized := must(contract.NewCommand(contract.CommandInput{OperationID: "op-agent", SourceCommandID: "src-agent", Actor: agent, Reference: c.current(),
			Carrier: c.carrier, Action: contract.ApproveConsent, Order: 1}))
		response, err := governance.ProcessConsent(t.Context(), w.store, governance.ConsentRequest{Command: unauthorized})
		if err != nil || response.Receipt.Result.Outcome() != contract.ConsentRejected || response.Receipt.Result.Reason() != contract.ConsentReasonUnauthorized || w.revision() != 10 {
			t.Fatalf("unauthorized approval %+v err=%v revision=%d", response, err, w.revision())
		}
		var outcome, reason, actorKind string
		if err := w.db.conn.QueryRow(t.Context(), "SELECT outcome, reason, actor_kind FROM consent_results WHERE source_command_id = 'src-agent'").Scan(&outcome, &reason, &actorKind); err != nil ||
			outcome != "rejected" || reason != "unauthorized" || actorKind != "agent" {
			t.Fatalf("stored rejection %q %q %q err=%v", outcome, reason, actorKind, err)
		}
	})
	t.Run("a revocation withdraws eligibility", func(t *testing.T) {
		response := w.consent(w.command(c, c.current().RevisionID, "op-revoke", "src-revoke", contract.RevokeConsent, 2))
		if response.Receipt.CurrentApprovalEligible || response.Receipt.Result.Outcome() != contract.ConsentRevoked || w.revision() != 11 {
			t.Fatalf("revocation %+v at revision %d", response, w.revision())
		}
		var outcome string
		if err := w.db.conn.QueryRow(t.Context(), "SELECT outcome FROM consent_results WHERE source_command_id = 'src-revoke'").Scan(&outcome); err != nil || outcome != "revoked" {
			t.Fatalf("stored outcome %q err=%v", outcome, err)
		}
	})
}

func TestOperationIdentityIsGlobal(t *testing.T) {
	w := newPGWorld(t)
	c := newCandidate("p1", "", w.protected("a"))
	other := newCandidateIn(fxProject, "suite-b", "p1", "", w.protected("a"))
	w.seedSuite(fxProject, fxSuite, c)
	w.seedSuite(fxProject, "suite-b", other)
	w.approve(c)

	if err := w.store.Do(t.Context(), fxProject, "suite-b", func(ctx context.Context, tx governance.Tx) error {
		found, ok, err := tx.Receipt(ctx, "approve-p1")
		if err != nil || !ok || found.SuiteID != fxSuite {
			t.Fatalf("receipt of another Suite %+v found=%v err=%v; want it visible from any Suite", found, ok, err)
		}
		bySource, ok, err := tx.ReceiptBySource(ctx, "comment-p1")
		if err != nil || !ok || bySource.SuiteID != fxSuite {
			t.Fatalf("source receipt of another Suite %+v found=%v err=%v", bySource, ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reuse := must(contract.NewCommand(contract.CommandInput{OperationID: "approve-p1", SourceCommandID: "another-source", Actor: w.owner, Reference: other.current(),
		Carrier: other.carrier, Action: contract.ApproveConsent, Order: 1}))
	if _, err := governance.ProcessConsent(t.Context(), w.store, governance.ConsentRequest{Command: reuse}); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("operation id of another Suite = %v; want ErrOperationConflict", err)
	}
	// Even when a caller skips the replay lookup, the store itself refuses the id.
	err := w.store.Do(t.Context(), fxProject, "suite-b", func(ctx context.Context, tx governance.Tx) error {
		write := w.consentWrite(other, "approve-p1", "yet-another-source", 1)
		return tx.AppendConsent(ctx, write)
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("AppendConsent with a used operation id = %v; want ErrOperationConflict", err)
	}
	// A source command id is global as well.
	err = w.store.Do(t.Context(), fxProject, "suite-b", func(ctx context.Context, tx governance.Tx) error {
		return tx.AppendConsent(ctx, w.consentWrite(other, "fresh-operation", "comment-p1", 1))
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("AppendConsent with a used source command = %v; want ErrOperationConflict", err)
	}
}

func TestConsentWriteRejectsForeignAndIncoherentFacts(t *testing.T) {
	w := newPGWorld(t)
	c := newCandidate("p1", "", w.protected("a"))
	other := newCandidateIn(fxProject, "suite-b", "p1", "", w.protected("a"))
	w.seedSuite(fxProject, fxSuite, c)
	w.seedSuite(fxProject, "suite-b", other)
	foreign := func(ctx context.Context, tx governance.Tx) error {
		write := w.consentWrite(c, "op-1", "src-1", 1)
		command := must(contract.NewCommand(contract.CommandInput{OperationID: "op-1", SourceCommandID: "src-1", Actor: w.owner, Reference: other.current(), Carrier: other.carrier, Action: contract.ApproveConsent, Order: 1}))
		write.Result = must(contract.ReconstituteCommandResult(command, contract.ConsentApproved, contract.ConsentReasonNone))
		return tx.AppendConsent(ctx, write)
	}
	if err := w.store.Do(t.Context(), fxProject, fxSuite, foreign); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("a result for another Suite = %v; want ErrInvalidRequest", err)
	}
	if w.count("consent_results") != 0 {
		t.Fatal("a result for another Suite was written under this Suite's lock")
	}
	err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		write := w.consentWrite(c, "op-1", "src-1", 1)
		write.OperationID = "different-from-the-command"
		return tx.AppendConsent(ctx, write) // not an alias, so the operation must be the command's own
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("a write that renames the operation without an alias = %v; want ErrOperationConflict", err)
	}
	// A genuine duplicate result, but for a source command this store never processed.
	processed := w.consentWrite(c, "op-source", "src-never-stored", 1)
	policy := must(contract.NewPolicy(fxProject, fxPolicy, w.owner))
	next, _, err := must(contract.NewConsent(fxProject, fxSuite, "p1")).Apply(c.proposal, policy, processed.Result.Command())
	if err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := next.Apply(c.proposal, policy, w.command(c, c.current().RevisionID, "op-alias", "src-never-stored", contract.ApproveConsent, 1))
	if err != nil || !duplicate.Duplicate() {
		t.Fatalf("fixture duplicate: %v", err)
	}
	err = w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.AppendConsent(ctx, governance.ConsentWrite{OperationID: "op-alias", Result: duplicate, Receipt: processed.Receipt, Alias: true})
	})
	if !errors.Is(err, governance.ErrInvalidState) || w.count("operations") != 0 {
		t.Fatalf("an alias for an unknown source command = %v; want ErrInvalidState and no row", err)
	}
	unknown := newCandidate("ghost-proposal", "", w.protected("a"))
	err = w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.AppendConsent(ctx, w.consentWrite(unknown, "op-ghost", "src-ghost", 1))
	})
	if !errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("a result for an unknown proposal = %v; want ErrNotFound", err)
	}
}

// promotionWrite builds the write a promotion use case would issue.
func (w *pgWorld) promotionWrite(c candidate, request governance.PromoteRequest, kind governance.OperationKind, corrects contract.SuiteVersionID) governance.PromotionWrite {
	w.t.Helper()
	record := must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: request.OperationID, VersionID: request.NewVersionID, Binding: c.proposal.Current().Binding(),
		Carrier: c.carrier, Source: request.Integration.Source(), Target: fxTarget, RecordedAt: request.RecordedAt, CorrectsVersionID: corrects}))
	version := must(contract.NewSuiteVersion(fxProject, fxSuite, request.NewVersionID, c.protected.Manifest()))
	return governance.PromotionWrite{
		Receipt: governance.PromotionReceipt{Identity: governance.PromotionIdentity{Kind: kind, Request: request, CorrectsVersionID: corrects, Binding: c.proposal.Current().Binding()}, Record: record},
		Version: version,
	}
}

func TestPromotionEndToEnd(t *testing.T) {
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("baseline"))
	one := newCandidate("p1", "v0", w.protected("first change"))
	two := newCandidate("p2", "v1", w.protected("second change"))
	w.seedWithBaseline(base, one, two)
	w.approve(one)
	if w.revision() != 4 {
		t.Fatalf("revision after the consent = %d; want 4", w.revision())
	}

	request := one.mergedRequest("promote-p1", "v1")
	result, err := governance.Promote(t.Context(), w.store, request)
	if err != nil || result.Outcome != contract.PromotionProposed || !result.Committed || result.Duplicate || w.revision() != 5 || w.currentVersion() != "v1" {
		t.Fatalf("promotion %+v err=%v revision=%d current=%q", result, err, w.revision(), w.currentVersion())
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		historical, found, err := tx.Version(ctx, "v1")
		if err != nil || !found || historical.Version().Manifest().Digest() != one.protected.Manifest().Digest() || !historical.Record().RecordedAt().Equal(fxRecordedAt) ||
			!historical.Record().Binding().Equal(one.proposal.Current().Binding()) || historical.Record().OperationID() != "promote-p1" {
			t.Fatalf("recorded version %+v found=%v err=%v", historical, found, err)
		}
		record, found, err := tx.PromotionFor(ctx, one.current())
		if err != nil || !found || record.VersionID() != "v1" {
			t.Fatalf("promotion for the proposal %+v found=%v err=%v", record, found, err)
		}
		stored, found, err := tx.Receipt(ctx, "promote-p1")
		if err != nil || !found || stored.Kind != governance.OperationPromote || stored.Promotion == nil || stored.Consent != nil || stored.ProjectID != fxProject || stored.SuiteID != fxSuite {
			t.Fatalf("promotion receipt %+v found=%v err=%v", stored, found, err)
		}
		identity := stored.Promotion.Identity
		if identity.Kind != governance.OperationPromote || identity.Request.OperationID != "promote-p1" || identity.Request.Reference != one.current() || identity.Request.Carrier != one.carrier ||
			!identity.Request.Proposed.Equal(one.protected) || identity.Request.AssessmentSource != one.merged || identity.Request.Integration != request.Integration ||
			identity.Request.NewVersionID != "v1" || !identity.Binding.Equal(one.proposal.Current().Binding()) || identity.CorrectsVersionID != "" ||
			stored.Promotion.Record.VersionID() != "v1" || !stored.Promotion.Record.RecordedAt().Equal(fxRecordedAt) {
			t.Fatalf("stored promotion identity %+v", identity)
		}
		return nil
	})

	replay, err := governance.Promote(t.Context(), w.store, request)
	if err != nil || !replay.Duplicate || !replay.Committed || replay.Record.VersionID() != "v1" || w.revision() != 5 {
		t.Fatalf("replay %+v err=%v revision=%d; want a duplicate without a write", replay, err, w.revision())
	}
	changed := request
	changed.NewVersionID = "v-other"
	if _, err := governance.Promote(t.Context(), w.store, changed); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("operation id reused for another version = %v; want ErrOperationConflict", err)
	}

	w.approve(two)
	correction := governance.CorrectionRequest{Promotion: two.mergedRequest("correct-p2", "v2"), TargetVersionID: "v0"}
	corrected, err := governance.Correct(t.Context(), w.store, correction)
	if err != nil || corrected.Outcome != contract.PromotionProposed || !corrected.Committed || w.currentVersion() != "v2" || w.revision() != 7 {
		t.Fatalf("correction %+v err=%v current=%q revision=%d", corrected, err, w.currentVersion(), w.revision())
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		historical, found, err := tx.Version(ctx, "v2")
		if err != nil || !found || historical.Record().CorrectsVersionID() != "v0" {
			t.Fatalf("corrected version %+v found=%v err=%v", historical, found, err)
		}
		stored, _, err := tx.Receipt(ctx, "correct-p2")
		if err != nil || stored.Kind != governance.OperationCorrect || stored.Promotion.Identity.CorrectsVersionID != "v0" {
			t.Fatalf("correction receipt %+v err=%v", stored, err)
		}
		return nil
	})
	var kind, corrects string
	if err := w.db.conn.QueryRow(t.Context(), "SELECT o.kind, p.corrects_version_id FROM operations o JOIN promotions p ON p.operation_id = o.operation_id WHERE o.operation_id = 'correct-p2'").Scan(&kind, &corrects); err != nil || kind != "correct" || corrects != "v0" {
		t.Fatalf("stored kind %q corrects %q err=%v", kind, corrects, err)
	}
	if n := w.count("suite_versions"); n != 3 {
		t.Fatalf("%d versions; want the baseline and two recorded versions", n)
	}
}

func TestBootstrapRecordsAnExistingBaseline(t *testing.T) {
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("baseline"))
	w.seedSuite(fxProject, fxSuite, base)
	w.approve(base)
	request := base.baselineRequest("bootstrap-p0", "v1")
	result, err := governance.Bootstrap(t.Context(), w.store, request)
	if err != nil || result.Outcome != contract.PromotionProposed || !result.Committed || w.currentVersion() != "v1" || w.revision() != 9 {
		t.Fatalf("bootstrap %+v err=%v current=%q revision=%d", result, err, w.currentVersion(), w.revision())
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		stored, found, err := tx.Receipt(ctx, "bootstrap-p0")
		if err != nil || !found || stored.Kind != governance.OperationBootstrap || stored.Promotion.Identity.Request.Integration.Kind() != contract.IntegrationExistingBaseline ||
			stored.Promotion.Identity.Request.Integration.Carrier() != "" {
			t.Fatalf("bootstrap receipt %+v found=%v err=%v", stored, found, err)
		}
		return nil
	})
	replay, err := governance.Bootstrap(t.Context(), w.store, request)
	if err != nil || !replay.Duplicate || w.revision() != 9 {
		t.Fatalf("bootstrap replay %+v err=%v revision=%d", replay, err, w.revision())
	}
	if _, err := governance.Promote(t.Context(), w.store, request); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("a bootstrap operation reused as a promotion = %v; want ErrOperationConflict", err)
	}
}

// readyForPromotion seeds a baseline Suite whose candidate is approved and ready.
func readyForPromotion(t *testing.T, w *pgWorld, protected contract.ProtectedContract) candidate {
	t.Helper()
	base := newCandidate("p0", "", w.protected("baseline"))
	one := newCandidate("p1", "v0", protected)
	w.seedWithBaseline(base, one)
	w.approve(one)
	return one
}

func TestConcurrentPromotionsRecordExactlyOneVersion(t *testing.T) {
	const contenders = 4
	run := func(t *testing.T, operations func(int) string, versions func(int) contract.SuiteVersionID) []governance.PromoteResult {
		w := newPGWorld(t)
		one := readyForPromotion(t, w, w.protected("contended"))
		before := w.revision()
		results := make([]governance.PromoteResult, contenders)
		failures := make([]error, contenders)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := range contenders {
			workers.Go(func() {
				<-start
				results[i], failures[i] = governance.Promote(t.Context(), w.store, one.mergedRequest(operations(i), versions(i)))
			})
		}
		close(start)
		workers.Wait()
		for i, err := range failures {
			if err != nil {
				t.Fatalf("contender %d: %v", i, err)
			}
		}
		if w.revision() != before+1 || w.count("suite_versions") != 2 || w.count("promotions") != 2 {
			t.Fatalf("revision %d (was %d), %d versions, %d promotions; want exactly one new version", w.revision(), before, w.count("suite_versions"), w.count("promotions"))
		}
		return results
	}
	t.Run("different operations", func(t *testing.T) {
		results := run(t, func(i int) string { return "promote-" + string(rune('a'+i)) }, func(i int) contract.SuiteVersionID { return contract.SuiteVersionID("v" + string(rune('1'+i))) })
		var winners, settled int
		for _, result := range results {
			switch {
			case result.Outcome == contract.PromotionProposed && result.Committed && !result.Duplicate:
				winners++
			case result.Outcome == contract.PromotionNoChange || result.Outcome == contract.PromotionBlocked:
				settled++
				if result.Committed {
					t.Fatalf("a losing contender reports a commit: %+v", result)
				}
			default:
				t.Fatalf("unexpected result %+v", result)
			}
		}
		if winners != 1 || settled != contenders-1 {
			t.Fatalf("%d winners and %d settled losers among %d contenders; want 1 and %d", winners, settled, contenders, contenders-1)
		}
	})
	t.Run("the same operation", func(t *testing.T) {
		results := run(t, func(int) string { return "promote-same" }, func(int) contract.SuiteVersionID { return "v1" })
		var originals, replays int
		for _, result := range results {
			if !result.Committed || result.Outcome != contract.PromotionProposed || result.Record.VersionID() != "v1" {
				t.Fatalf("unexpected result %+v", result)
			}
			if result.Duplicate {
				replays++
			} else {
				originals++
			}
		}
		if originals != 1 || replays != contenders-1 {
			t.Fatalf("%d originals and %d replays; want 1 and %d", originals, replays, contenders-1)
		}
	})
}

func TestUnitsOfWorkOnOneSuiteRunOneAtATime(t *testing.T) {
	w := newPGWorld(t)
	w.seedSuite(fxProject, fxSuite, newCandidate("p1", "", w.protected("a")))
	entered, release := make(chan struct{}), make(chan struct{})
	var secondRan atomic.Bool
	var workers sync.WaitGroup
	workers.Go(func() {
		if err := w.store.Do(t.Context(), fxProject, fxSuite, func(context.Context, governance.Tx) error {
			close(entered)
			<-release
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	<-entered
	workers.Go(func() {
		if err := w.store.Do(t.Context(), fxProject, fxSuite, func(context.Context, governance.Tx) error {
			secondRan.Store(true)
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	// Wait until PostgreSQL reports the second unit of work blocked on the Suite lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := w.db.conn.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%FOR UPDATE%'").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			workers.Wait()
			t.Fatal("the second unit of work never blocked on the Suite lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if secondRan.Load() {
		close(release)
		workers.Wait()
		t.Fatal("a second unit of work ran while the first held the Suite lock")
	}
	close(release)
	workers.Wait()
	if !secondRan.Load() {
		t.Fatal("the second unit of work never ran after the lock was released")
	}
}

func TestEveryStatementOfAUnitOfWorkRollsBackTogether(t *testing.T) {
	w := newPGWorld(t)
	one := readyForPromotion(t, w, w.protected("change"))
	revision, versions, operations := w.revision(), w.count("suite_versions"), w.count("operations")
	t.Run("a later consent statement fails", func(t *testing.T) {
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			if err := tx.AppendConsent(ctx, w.consentWrite(one, "op-first", "src-first", 2)); err != nil {
				return err
			}
			return tx.AppendConsent(ctx, w.consentWrite(one, "op-second", "src-first", 3)) // the source command is already used
		})
		if !errors.Is(err, governance.ErrOperationConflict) || w.revision() != revision || w.count("operations") != operations {
			t.Fatalf("Do = %v at revision %d with %d operations; want ErrOperationConflict and the earlier write undone", err, w.revision(), w.count("operations"))
		}
	})
	t.Run("the promotion fails after its first rows were inserted", func(t *testing.T) {
		write := w.promotionWrite(one, one.mergedRequest("promote-bad", "v9"), governance.OperationCorrect, "no-such-version")
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			return tx.RecordPromotion(ctx, write)
		})
		if !errors.Is(err, governance.ErrNotFound) || w.revision() != revision || w.count("suite_versions") != versions || w.count("operations") != operations || w.currentVersion() != "v0" {
			t.Fatalf("Do = %v at revision %d, %d versions, %d operations; want ErrNotFound (unknown corrected version) with no partial effect", err, w.revision(), w.count("suite_versions"), w.count("operations"))
		}
	})
}

func TestVersionAndOperationConflictsAreTranslated(t *testing.T) {
	w := newPGWorld(t)
	one := readyForPromotion(t, w, w.protected("change"))
	revision := w.revision()
	cases := []struct {
		name    string
		write   governance.PromotionWrite
		wantErr error
	}{
		{"a version id that already exists", w.promotionWrite(newCandidateFromBase(w), newCandidateFromBase(w).mergedRequest("promote-a", "v0"), governance.OperationPromote, ""), governance.ErrVersionConflict},
		{"a promoted reference promoted again", w.promotionWrite(newCandidateFromBase(w), newCandidateFromBase(w).mergedRequest("promote-b", "v7"), governance.OperationPromote, ""), governance.ErrVersionConflict},
		{"an operation id used by seeded history", w.promotionWrite(one, one.mergedRequest("seed-promote", "v8"), governance.OperationPromote, ""), governance.ErrOperationConflict},
		{"an operation id used by a consent", w.promotionWrite(one, one.mergedRequest("approve-p1", "v8"), governance.OperationPromote, ""), governance.ErrOperationConflict},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error { return tx.RecordPromotion(ctx, tt.write) })
			if !errors.Is(err, tt.wantErr) || w.revision() != revision {
				t.Fatalf("RecordPromotion = %v at revision %d; want %v and no write", err, w.revision(), tt.wantErr)
			}
		})
	}
}

// newCandidateFromBase rebuilds the baseline candidate of readyForPromotion.
func newCandidateFromBase(w *pgWorld) candidate {
	return newCandidate("p0", "", unstoredProtected("baseline"))
}

func TestPromotionWritesRejectForeignFacts(t *testing.T) {
	w := newPGWorld(t)
	one := readyForPromotion(t, w, w.protected("change"))
	revision := w.revision()
	otherSuite := func(write governance.PromotionWrite) governance.PromotionWrite {
		write.Version = must(contract.NewSuiteVersion(fxProject, "suite-b", write.Version.ID(), write.Version.Manifest()))
		return write
	}
	nanoseconds := one.mergedRequest("promote-ns", "v5")
	nanoseconds.RecordedAt = fxRecordedAt.Add(7 * time.Nanosecond)
	wrongCorrects := w.promotionWrite(one, one.mergedRequest("promote-z", "v5"), governance.OperationPromote, "")
	wrongCorrects.Receipt.Identity.CorrectsVersionID = "v0" // the record corrects nothing
	wrongBinding := w.promotionWrite(one, one.mergedRequest("promote-w", "v5"), governance.OperationPromote, "")
	wrongBinding.Receipt.Identity.Binding = newCandidateFromBase(w).proposal.Current().Binding() // another proposal's binding
	cases := []struct {
		name  string
		write governance.PromotionWrite
	}{
		{"a version of another Suite", otherSuite(w.promotionWrite(one, one.mergedRequest("promote-x", "v5"), governance.OperationPromote, ""))},
		{"an unknown operation kind", w.promotionWrite(one, one.mergedRequest("promote-y", "v5"), governance.OperationConsent, "")},
		{"a time PostgreSQL cannot store exactly", w.promotionWrite(one, nanoseconds, governance.OperationPromote, "")},
		{"an identity that corrects another version than the record", wrongCorrects},
		{"an identity bound to another proposal than the record", wrongBinding},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error { return tx.RecordPromotion(ctx, tt.write) })
			if !errors.Is(err, governance.ErrInvalidRequest) || w.revision() != revision {
				t.Fatalf("RecordPromotion = %v at revision %d; want ErrInvalidRequest and no write", err, w.revision())
			}
		})
	}
}

func TestArtifactBytesAreVerifiedWhenAVersionIsWritten(t *testing.T) {
	assertNothingWritten := func(t *testing.T, w *pgWorld, revision contract.StateRevision) {
		t.Helper()
		if w.revision() != revision || w.count("suite_versions") != 1 || w.count("promotions") != 1 || w.count("operations") != 1 || w.currentVersion() != "v0" {
			t.Fatalf("revision %d (was %d), %d versions, %d promotions, %d operations, current %q; want no partial effect",
				w.revision(), revision, w.count("suite_versions"), w.count("promotions"), w.count("operations"), w.currentVersion())
		}
	}
	t.Run("missing content", func(t *testing.T) {
		w := newPGWorld(t)
		one := readyForPromotion(t, w, unstoredProtected("never stored"))
		revision := w.revision()
		_, err := governance.Promote(t.Context(), w.store, one.mergedRequest("promote-p1", "v1"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Promote with missing content = %v; want the content store's not-exist cause", err)
		}
		assertNothingWritten(t, w, revision)
	})
	t.Run("corrupt content", func(t *testing.T) {
		root := t.TempDir()
		w := newPGWorld(t)
		content, err := filesystem.NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if w.store, err = postgres.NewStore(w.pool, content, testInserter()); err != nil {
			t.Fatal(err)
		}
		w.content = content
		protected := w.protected("will be damaged")
		one := readyForPromotion(t, w, protected)
		revision := w.revision()
		object := filepath.Join(root, "sha256-"+strings.TrimPrefix(protected.Manifest().Entries()[0].Content.String(), "sha256:"))
		if err := os.Remove(object); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(object, []byte("damaged"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = governance.Promote(t.Context(), w.store, one.mergedRequest("promote-p1", "v1"))
		if !errors.Is(err, filesystem.ErrCorruptArtifact) {
			t.Fatalf("Promote with corrupt content = %v; want ErrCorruptArtifact", err)
		}
		assertNothingWritten(t, w, revision)

		// Reads never re-hash content: a version written earlier stays readable after its bytes are damaged.
		if err := os.WriteFile(object, []byte("will be damaged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := governance.Promote(t.Context(), w.store, one.mergedRequest("promote-p1", "v1")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(object, []byte("damaged again"), 0o600); err != nil {
			t.Fatal(err)
		}
		w.read(func(ctx context.Context, tx governance.Tx) error {
			if _, found, err := tx.Version(ctx, "v1"); err != nil || !found {
				t.Fatalf("version read after its content was damaged: found=%v err=%v", found, err)
			}
			if _, err := tx.Suite(ctx); err != nil {
				t.Fatalf("suite read after its content was damaged: %v", err)
			}
			return nil
		})
	})
}

func TestReadsRejectStoredStateThatContradictsItself(t *testing.T) {
	w := newPGWorld(t)
	readyForPromotion(t, w, w.protected("change"))
	for _, statement := range []string{
		"ALTER TABLE suite_versions DISABLE TRIGGER suite_versions_immutable",
		"UPDATE suite_versions SET manifest_digest = 'sha256:" + strings.Repeat("0", 64) + "' WHERE version_id = 'v0'",
		"ALTER TABLE suite_versions ENABLE TRIGGER suite_versions_immutable",
	} {
		if _, err := w.db.conn.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		if _, _, err := tx.Version(ctx, "v0"); !errors.Is(err, governance.ErrInvalidState) {
			t.Fatalf("Version over a manifest that does not match its digest = %v; want ErrInvalidState", err)
		}
		if _, err := tx.Suite(ctx); !errors.Is(err, governance.ErrInvalidState) {
			t.Fatalf("Suite over a manifest that does not match its digest = %v; want ErrInvalidState", err)
		}
		return nil
	})
}
