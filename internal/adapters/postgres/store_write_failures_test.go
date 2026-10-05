//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// exec runs a statement as the owner of the isolated schema.
func (w *pgWorld) exec(statement string) {
	w.t.Helper()
	if _, err := w.db.conn.Exec(w.t.Context(), statement); err != nil {
		w.t.Fatalf("%q: %v", statement, err)
	}
}

// refuseAtCommit makes every statement succeed and the commit fail, by a
// deferred trigger on the table that raises the given SQLSTATE.
func (w *pgWorld) refuseAtCommit(table, event, sqlState string) {
	w.t.Helper()
	w.exec("CREATE FUNCTION refuse_at_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused at commit' USING ERRCODE = '" + sqlState + "'; END; $$")
	w.exec("CREATE CONSTRAINT TRIGGER refuse_at_commit AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION refuse_at_commit()")
}

func TestDoRefusesWorkItCannotStartOrCommit(t *testing.T) {
	t.Run("a missing callback", func(t *testing.T) {
		w := newPGWorld(t)
		w.seedSuite(fxProject, fxSuite)
		if err := w.store.Do(t.Context(), fxProject, fxSuite, nil); !errors.Is(err, governance.ErrInvalidRequest) {
			t.Fatalf("Do without a callback = %v; want ErrInvalidRequest", err)
		}
	})
	t.Run("a closed pool", func(t *testing.T) {
		w := newPGWorld(t)
		w.seedSuite(fxProject, fxSuite)
		w.pool.Close()
		called := false
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(context.Context, governance.Tx) error { called = true; return nil })
		if err == nil || called || !strings.Contains(err.Error(), "begin governance transaction") {
			t.Fatalf("Do on a closed pool = %v (callback ran: %v); want a begin failure", err, called)
		}
	})
	t.Run("a commit the database refuses", func(t *testing.T) {
		w := newPGWorld(t)
		c := newCandidate("p1", "", w.protected("a"))
		w.seedSuite(fxProject, fxSuite, c)
		w.refuseAtCommit("suites", "UPDATE", "23514")
		err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
			return tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1))
		})
		if err == nil || !strings.Contains(err.Error(), "commit governance transaction") {
			t.Fatalf("Do = %v; want the commit failure", err)
		}
		if w.count("consent_results") != 0 || w.count("operations") != 0 || w.revision() != 7 {
			t.Fatalf("%d results, %d operations at revision %d after a refused commit; want none at 7", w.count("consent_results"), w.count("operations"), w.revision())
		}
	})
}

func TestConsentWriteRefusesIncoherentResults(t *testing.T) {
	w := newPGWorld(t)
	c := newCandidate("p1", "", w.protected("a"))
	w.seedSuite(fxProject, fxSuite, c)
	policy := must(contract.NewPolicy(fxProject, fxPolicy, w.owner))
	original := w.consentWrite(c, "op-1", "src-1", 1)
	next, _, err := must(contract.NewConsent(fxProject, fxSuite, "p1")).Apply(c.proposal, policy, original.Result.Command())
	if err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := next.Apply(c.proposal, policy, w.command(c, c.current().RevisionID, "op-2", "src-1", contract.ApproveConsent, 1))
	if err != nil || !duplicate.Duplicate() {
		t.Fatalf("fixture duplicate: %v", err)
	}
	otherSource := w.consentWrite(c, "op-3", "src-3", 2)
	maxOrder := w.consentWrite(c, "op-4", "src-4", math.MaxUint64)

	noOperation := original
	noOperation.OperationID = ""
	foreignReceipt := original
	foreignReceipt.Receipt = otherSource.Receipt
	duplicateReceipt := original
	duplicateReceipt.Receipt = governance.ConsentReceipt{Result: duplicate, EvaluatedReference: c.current(), PolicyRevisionID: fxPolicy}
	cases := []struct {
		name  string
		write governance.ConsentWrite
	}{
		{"a write without its operation", noOperation},
		{"a receipt of another source command", foreignReceipt},
		{"a receipt that records a duplicate result", duplicateReceipt},
		{"a command order beyond the stored range", maxOrder},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error { return tx.AppendConsent(ctx, tt.write) })
			if !errors.Is(err, governance.ErrInvalidRequest) || w.count("consent_results") != 0 || w.count("operations") != 0 || w.revision() != 7 {
				t.Fatalf("AppendConsent = %v with %d results at revision %d; want ErrInvalidRequest and no write", err, w.count("consent_results"), w.revision())
			}
		})
	}
}

func TestWritesThatCannotAdvanceTheRevisionAreUndone(t *testing.T) {
	w := newPGWorld(t)
	c := newCandidate("p1", "", w.protected("a"))
	w.seedSuite(fxProject, fxSuite, c)
	w.exec("ALTER TABLE suites ADD CONSTRAINT suites_revision_ceiling CHECK (revision <= 7)")
	err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.AppendConsent(ctx, w.consentWrite(c, "op-1", "src-1", 1))
	})
	if err == nil || !strings.Contains(err.Error(), "advance suite revision") {
		t.Fatalf("AppendConsent = %v; want the failure to advance the revision", err)
	}
	if w.count("consent_results") != 0 || w.count("operations") != 0 {
		t.Fatalf("%d results and %d operations remain after the revision could not advance", w.count("consent_results"), w.count("operations"))
	}
}

func TestPromotionWriteRefusesIncoherentFacts(t *testing.T) {
	w := newPGWorld(t)
	one := readyForPromotion(t, w, w.protected("change"))
	revision := w.revision()
	noVersion := w.promotionWrite(one, one.mergedRequest("promote-a", "v5"), governance.OperationPromote, "")
	noVersion.Version = contract.SuiteVersion{}
	noRecord := w.promotionWrite(one, one.mergedRequest("promote-b", "v5"), governance.OperationPromote, "")
	noRecord.Receipt.Record = contract.PromotionRecord{}
	otherOperation := w.promotionWrite(one, one.mergedRequest("promote-c", "v5"), governance.OperationPromote, "")
	otherOperation.Receipt.Identity.Request.OperationID = "promote-another"
	otherManifest := w.promotionWrite(one, one.mergedRequest("promote-d", "v5"), governance.OperationPromote, "")
	otherManifest.Version = must(contract.NewSuiteVersion(fxProject, fxSuite, "v5", unstoredProtected("another contract").Manifest()))
	cases := []struct {
		name  string
		write governance.PromotionWrite
	}{
		{"a write without a version", noVersion},
		{"a write without a record", noRecord},
		{"a request and a record of different operations", otherOperation},
		{"a version whose manifest is not the approved one", otherManifest},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := w.store.Do(t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error { return tx.RecordPromotion(ctx, tt.write) })
			if !errors.Is(err, governance.ErrInvalidRequest) || w.revision() != revision || w.count("suite_versions") != 1 {
				t.Fatalf("RecordPromotion = %v at revision %d; want ErrInvalidRequest and no write", err, w.revision())
			}
		})
	}
}

// countingVerifier counts the content checks a promotion makes.
type countingVerifier struct {
	inner postgres.ArtifactVerifier
	calls atomic.Int32
}

func (v *countingVerifier) Verify(ctx context.Context, digest artifact.Digest) error {
	v.calls.Add(1)
	return v.inner.Verify(ctx, digest)
}

func TestPromotionVerifiesEachDistinctContentOnce(t *testing.T) {
	w := newPGWorld(t)
	verifier := &countingVerifier{inner: w.content}
	var err error
	if w.store, err = postgres.NewStore(w.pool, verifier); err != nil {
		t.Fatal(err)
	}
	shared := artifact.Hash([]byte("shared bytes"))
	if err := w.content.Put(t.Context(), shared, strings.NewReader("shared bytes")); err != nil {
		t.Fatal(err)
	}
	manifest := must(artifact.NewManifest([]artifact.Entry{{Path: "tests/a.txt", Content: shared}, {Path: "tests/b.txt", Content: shared}}))
	protected := must(contract.NewProtectedContract(manifest, artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
	one := readyForPromotion(t, w, protected)
	result, err := governance.Promote(t.Context(), w.store, one.mergedRequest("promote-p1", "v1"))
	if err != nil || !result.Committed {
		t.Fatalf("Promote = %+v, %v", result, err)
	}
	if got := verifier.calls.Load(); got != 1 {
		t.Fatalf("content verified %d times for two entries with the same bytes; want once", got)
	}
}

func TestSeedRefusesStateThatContradictsItself(t *testing.T) {
	w := newPGWorld(t)
	one := newCandidate("p1", "", w.protected("one"))
	base := newCandidate("p0", "", w.protected("baseline"))
	foreign := newCandidateIn(fxProject, "suite-b", "p1", "", base.protected)
	otherSuiteVersion := must(contract.NewSuiteVersion(fxProject, "suite-b", "v0", base.protected.Manifest()))
	foreignBinding := foreign.proposal.Current().Binding()
	foreignRecord := must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "seed-promote", VersionID: "v0", Binding: foreignBinding,
		Carrier: foreign.carrier, Source: foreign.merged, Target: fxTarget, RecordedAt: fxRecordedAt}))

	noTarget := w.emptySeed(fxProject, fxSuite, one)
	noTarget.Suite.Target = ""
	otherProjectPolicy := w.emptySeed(fxProject, fxSuite, one)
	otherProjectPolicy.Suite.Policy = must(contract.NewPolicy("project-b", fxPolicy, w.owner))
	historyOfAnotherSuite := w.baselineSeed(base)
	historyOfAnotherSuite.History = []contract.HistoricalCanonical{must(contract.NewHistoricalCanonical(otherSuiteVersion, foreignRecord))}
	preciseHistory := w.baselineSeed(base)
	version := preciseHistory.History[0].Version()
	preciseHistory.History = []contract.HistoricalCanonical{must(contract.NewHistoricalCanonical(version, must(contract.NewPromotionRecord(contract.PromotionRecordInput{
		OperationID: "seed-promote", VersionID: "v0", Binding: base.proposal.Current().Binding(), Carrier: base.carrier, Source: base.merged, Target: fxTarget,
		RecordedAt: fxRecordedAt.Add(7 * time.Nanosecond)}))))}
	bareProposal := w.emptySeed(fxProject, fxSuite, one)
	bareProposal.Proposals = append(bareProposal.Proposals, governance.SeedProposal{})
	foreignProposal := w.emptySeed(fxProject, fxSuite, one)
	foreignProposal.Proposals = append(foreignProposal.Proposals, governance.SeedProposal{Proposal: foreign.proposal})

	cases := []struct {
		name string
		seed governance.Seed
	}{
		{"nothing at all", governance.Seed{}},
		{"a Suite without a target", noTarget},
		{"a policy of another project", otherProjectPolicy},
		{"a history entry of another Suite", historyOfAnotherSuite},
		{"a history entry recorded at a time PostgreSQL cannot store", preciseHistory},
		{"a proposal without revisions", bareProposal},
		{"a proposal of another Suite", foreignProposal},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := w.store.Seed(t.Context(), tt.seed); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("Seed = %v; want ErrInvalidRequest", err)
			}
			for _, table := range governanceTables {
				if n := w.count(table); n != 0 {
					t.Fatalf("%d rows remain in %s after a rejected seed", n, table)
				}
			}
		})
	}
}

func TestSeedRefusesWhatTheSchemaRefusesAndLeavesNothing(t *testing.T) {
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("baseline"))
	one := newCandidate("p1", "v0", w.protected("change"))
	seed := w.baselineSeed(base, one)
	// Each table refuses its first row, so the seed fails at that statement.
	for _, table := range []struct{ name, statement string }{
		{"policies", "insert policy"}, {"proposals", "insert proposal p0"}, {"proposal_revisions", "insert revision revision-1"}, {"suite_versions", "insert version v0"},
		{"promotions", "insert promotion of version v0"}, {"assessments", "insert assessment"},
	} {
		t.Run(table.name, func(t *testing.T) {
			w := newPGWorld(t)
			w.exec("ALTER TABLE " + table.name + " ADD CONSTRAINT refuse_everything CHECK (false) NOT VALID")
			err := w.store.Seed(t.Context(), seed)
			if !errors.Is(err, governance.ErrInvalidRequest) || !strings.Contains(err.Error(), table.statement) {
				t.Fatalf("Seed = %v; want ErrInvalidRequest at %q", err, table.statement)
			}
			for _, name := range governanceTables {
				if n := w.count(name); n != 0 {
					t.Fatalf("%d rows remain in %s after the seed failed at %s", n, name, table.name)
				}
			}
		})
	}
	t.Run("a policy table that cannot be read", func(t *testing.T) {
		w := newPGWorld(t)
		w.exec(renameTable("policies"))
		if err := w.store.Seed(t.Context(), seed); err == nil || errors.Is(err, governance.ErrInvalidRequest) || !strings.Contains(err.Error(), "load policy") {
			t.Fatalf("Seed = %v; want a storage failure at the policy lookup", err)
		}
	})
	t.Run("a commit the schema refuses", func(t *testing.T) {
		w := newPGWorld(t)
		w.refuseAtCommit("suites", "INSERT", "23514")
		if err := w.store.Seed(t.Context(), seed); !errors.Is(err, governance.ErrInvalidRequest) || !strings.Contains(err.Error(), "commit seed") {
			t.Fatalf("Seed = %v; want ErrInvalidRequest at the commit", err)
		}
		if n := w.count("suites") + w.count("policies"); n != 0 {
			t.Fatalf("%d rows remain after a refused seed commit", n)
		}
	})
	t.Run("a commit that fails for another reason", func(t *testing.T) {
		w := newPGWorld(t)
		w.refuseAtCommit("suites", "INSERT", "P0001")
		if err := w.store.Seed(t.Context(), seed); err == nil || errors.Is(err, governance.ErrInvalidRequest) || !strings.Contains(err.Error(), "commit seed") {
			t.Fatalf("Seed = %v; want a commit failure that is not a rejected seed", err)
		}
	})
	t.Run("a canceled context", func(t *testing.T) {
		w := newPGWorld(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := w.store.Seed(ctx, seed); !errors.Is(err, context.Canceled) || w.count("suites") != 0 {
			t.Fatalf("Seed with a canceled context = %v; want context.Canceled and no rows", err)
		}
	})
	t.Run("a closed pool", func(t *testing.T) {
		w := newPGWorld(t)
		w.pool.Close()
		if err := w.store.Seed(t.Context(), seed); err == nil || !strings.Contains(err.Error(), "begin seed transaction") {
			t.Fatalf("Seed on a closed pool = %v; want a begin failure", err)
		}
	})
}
