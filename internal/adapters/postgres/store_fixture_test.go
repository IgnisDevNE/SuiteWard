//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	fxProject = contract.ProjectID("project")
	fxSuite   = contract.SuiteID("suite")
	fxPolicy  = contract.PolicyRevisionID("policy-1")
	fxTarget  = contract.IntegrationTargetID("default")
)

// fxRecordedAt has microsecond precision, which is all PostgreSQL stores.
var fxRecordedAt = time.Date(2026, 10, 2, 12, 0, 0, 123456000, time.UTC)

// must unwraps a fixture value; a failed fixture panics and fails the test.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// candidate is a proposal with passing integrity assessments for its
// candidate source and its merged source.
type candidate struct {
	id          string
	baseline    contract.SuiteVersionID
	protected   contract.ProtectedContract
	proposal    contract.Proposal
	carrier     contract.ApprovalCarrierID
	origin      contract.SourceRevision
	merged      contract.SourceRevision
	assessments []contract.IntegrityAssessment
}

// pgWorld is one isolated schema with the migrated governance tables, a real
// content store and a Store over a connection pool.
type pgWorld struct {
	t       *testing.T
	db      schemaDatabase
	pool    *pgxpool.Pool
	content *filesystem.Store
	store   *postgres.Store
	owner   contract.Principal
}

func newPGWorld(t *testing.T) *pgWorld {
	t.Helper()
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	content, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewStore(pool, content)
	if err != nil {
		t.Fatalf("NewStore on the migrated schema: %v", err)
	}
	return &pgWorld{t: t, db: database, pool: pool, content: content, store: store, owner: must(contract.NewPrincipal("owner", contract.Human))}
}

// protected stores the content bytes and returns the contract that covers them.
func (w *pgWorld) protected(text string) contract.ProtectedContract {
	w.t.Helper()
	protected := unstoredProtected(text)
	for _, entry := range protected.Manifest().Entries() {
		if err := w.content.Put(w.t.Context(), entry.Content, bytes.NewReader([]byte(text))); err != nil {
			w.t.Fatal(err)
		}
	}
	return protected
}

// unstoredProtected covers content that was never written to the content store.
func unstoredProtected(text string) contract.ProtectedContract {
	manifest := must(artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash([]byte(text))}}))
	return must(contract.NewProtectedContract(manifest, artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
}

// newCandidate builds one proposal; the last revision is the current one.
func newCandidate(id string, baseline contract.SuiteVersionID, protected contract.ProtectedContract, revisions ...contract.ProposalRevisionID) candidate {
	return newCandidateIn(fxProject, fxSuite, id, baseline, protected, revisions...)
}

func newCandidateIn(project contract.ProjectID, suite contract.SuiteID, id string, baseline contract.SuiteVersionID, protected contract.ProtectedContract, revisions ...contract.ProposalRevisionID) candidate {
	if len(revisions) == 0 {
		revisions = []contract.ProposalRevisionID{"revision-1"}
	}
	c := candidate{id: id, baseline: baseline, protected: protected, carrier: contract.ApprovalCarrierID("carrier-" + id),
		origin: contract.SourceRevision("candidate-" + id), merged: contract.SourceRevision("merged-" + id)}
	for _, revisionID := range revisions {
		reference := contract.ProposalReference{ProjectID: project, SuiteID: suite, ProposalID: contract.ProposalID(id), RevisionID: revisionID}
		binding := must(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: baseline,
			Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: fxPolicy, CoveredInputs: protected.CoveredInputs()}))
		revision := must(contract.NewProposalRevision(binding, c.origin, c.carrier))
		if c.proposal.IsZero() {
			c.proposal = must(contract.NewProposal(revision))
		} else {
			c.proposal = must(c.proposal.Revise(revision))
		}
		for _, source := range []contract.SourceRevision{c.origin, c.merged} {
			evidence := must(contract.NewIntegrityEvidence("verifier", source, binding, contract.IntegrityPassed))
			c.assessments = append(c.assessments, must(contract.AssessIntegrity(source, binding, &evidence)))
		}
	}
	return c
}

func (c candidate) current() contract.ProposalReference {
	return c.proposal.Current().Binding().Reference()
}

func (c candidate) mergedRequest(operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	integration := must(contract.NewIntegration(c.current().ProjectID, fxTarget, c.merged, c.carrier, contract.IntegrationMergedChange))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.merged, Integration: integration, NewVersionID: version, RecordedAt: fxRecordedAt}
}

func (c candidate) baselineRequest(operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	integration := must(contract.NewIntegration(c.current().ProjectID, fxTarget, c.origin, "", contract.IntegrationExistingBaseline))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.origin, Integration: integration, NewVersionID: version, RecordedAt: fxRecordedAt}
}

// seedSuite seeds a Suite at revision 7 without a canonical version, owned by
// one human, with every candidate admitted to the schedule in order.
func (w *pgWorld) seedSuite(project contract.ProjectID, suite contract.SuiteID, candidates ...candidate) {
	w.t.Helper()
	if err := w.store.Seed(w.t.Context(), w.emptySeed(project, suite, candidates...)); err != nil {
		w.t.Fatalf("seed %s/%s: %v", project, suite, err)
	}
}

func (w *pgWorld) emptySeed(project contract.ProjectID, suite contract.SuiteID, candidates ...candidate) governance.Seed {
	policy := must(contract.NewPolicy(project, fxPolicy, w.owner))
	canonical := must(contract.NewCanonicalSnapshot(must(contract.NewSuite(project, suite, "", 7)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	schedule := must(contract.NewSchedule(project, suite))
	seed := governance.Seed{}
	for _, c := range candidates {
		schedule = must(schedule.Admit(c.proposal, true))
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
	}
	seed.Suite = governance.SuiteState{Canonical: canonical, Policy: policy, Target: fxTarget, Schedule: schedule}
	return seed
}

// seedWithBaseline seeds a Suite whose canonical version "v0" was promoted by
// the already integrated candidate base; the other candidates follow it.
func (w *pgWorld) seedWithBaseline(base candidate, candidates ...candidate) {
	w.t.Helper()
	version := must(contract.NewSuiteVersion(fxProject, fxSuite, "v0", base.protected.Manifest()))
	record := must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "seed-promote", VersionID: "v0", Binding: base.proposal.Current().Binding(),
		Carrier: base.carrier, Source: base.merged, Target: fxTarget, RecordedAt: fxRecordedAt}))
	canonical := must(contract.NewCanonicalSnapshot(must(contract.NewSuite(fxProject, fxSuite, "v0", 3)), version, base.protected, record))
	schedule := must(contract.NewSchedule(fxProject, fxSuite))
	schedule = must(schedule.Admit(base.proposal, true))
	schedule = must(schedule.Observe(base.proposal, contract.ObservePromoted))
	seed := governance.Seed{History: []contract.HistoricalCanonical{must(contract.NewHistoricalCanonical(version, record))}}
	seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: base.proposal, Assessments: base.assessments})
	for _, c := range candidates {
		schedule = must(schedule.Admit(c.proposal, true))
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
	}
	seed.Suite = governance.SuiteState{Canonical: canonical, Policy: must(contract.NewPolicy(fxProject, fxPolicy, w.owner)), Target: fxTarget, Schedule: schedule}
	if err := w.store.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatalf("seed with baseline: %v", err)
	}
}

func (w *pgWorld) command(c candidate, revision contract.ProposalRevisionID, operation, source string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
	w.t.Helper()
	reference := c.current()
	reference.RevisionID = revision
	return must(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(operation), SourceCommandID: contract.SourceCommandID(source),
		Actor: w.owner, Reference: reference, Carrier: c.carrier, Action: action, Order: order}))
}

func (w *pgWorld) consent(command contract.Command) governance.ConsentResponse {
	w.t.Helper()
	response, err := governance.ProcessConsent(w.t.Context(), w.store, governance.ConsentRequest{Command: command})
	if err != nil {
		w.t.Fatalf("process consent: %v", err)
	}
	return response
}

// approve records the owner's approval of the candidate's current revision.
func (w *pgWorld) approve(c candidate) {
	w.t.Helper()
	response := w.consent(w.command(c, c.current().RevisionID, "approve-"+c.id, "comment-"+c.id, contract.ApproveConsent, 1))
	if !response.Receipt.CurrentApprovalEligible {
		w.t.Fatalf("approval of %s is not eligible: %v", c.id, response.Receipt.Result.Reason())
	}
}

// read runs fn in a unit of work that writes nothing.
func (w *pgWorld) read(fn func(context.Context, governance.Tx) error) {
	w.t.Helper()
	if err := w.store.Do(w.t.Context(), fxProject, fxSuite, fn); err != nil {
		w.t.Fatal(err)
	}
}

func (w *pgWorld) state() governance.SuiteState {
	w.t.Helper()
	var state governance.SuiteState
	w.read(func(ctx context.Context, tx governance.Tx) (err error) {
		state, err = tx.Suite(ctx)
		return err
	})
	return state
}

func (w *pgWorld) revision() contract.StateRevision { return w.state().Canonical.Suite().Revision() }

func (w *pgWorld) currentVersion() contract.SuiteVersionID {
	id, _ := w.state().Canonical.Suite().CurrentVersionID()
	return id
}

func (w *pgWorld) count(table string) int {
	w.t.Helper()
	var n int
	if err := w.db.conn.QueryRow(w.t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}
