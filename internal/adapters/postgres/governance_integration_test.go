//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func pgValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func pgInitializeAndApprove(t *testing.T, store *postgres.Store, fixture governanceFixture) {
	t.Helper()
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatal(err)
	}
	if result, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command}); err != nil || !result.Committed {
		t.Fatalf("approve setup: %+v %v", result, err)
	}
}

type loadedBarrier struct {
	governance.Store
	ready   chan<- struct{}
	release <-chan struct{}
}

func (s loadedBarrier) Load(ctx context.Context, r governance.ReadRequest) (governance.Snapshot, error) {
	snapshot, err := s.Store.Load(ctx, r)
	if err != nil {
		return snapshot, err
	}
	select {
	case s.ready <- struct{}{}:
	case <-ctx.Done():
		return governance.Snapshot{}, ctx.Err()
	}
	select {
	case <-s.release:
		return snapshot, nil
	case <-ctx.Done():
		return governance.Snapshot{}, ctx.Err()
	}
}
func TestPostgresPromotionRacePreservesOneWinner(t *testing.T) {
	pool, store, _ := newGovernanceDatabase(t)
	fixture := pgGovernanceFixture(t)
	pgInitializeAndApprove(t, store, fixture)
	ready, release := make(chan struct{}, 2), make(chan struct{})
	barrier := loadedBarrier{store, ready, release}
	var wait sync.WaitGroup
	results := make([]governance.PromoteResult, 2)
	failures := make([]error, 2)
	for i := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := fixture.request
			request.OperationID = contract.OperationID("race-" + string(rune('a'+i)))
			request.NewVersionID = contract.SuiteVersionID("race-version-" + string(rune('a'+i)))
			results[i], failures[i] = governance.Promote(t.Context(), barrier, request)
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	close(release)
	wait.Wait()
	winner := -1
	for i, result := range results {
		if failures[i] == nil && result.Committed {
			if winner != -1 {
				t.Fatal("two promotions committed")
			}
			winner = i
		} else if !errors.Is(failures[i], governance.ErrAuthorityConflict) {
			t.Fatalf("loser did not reject stale whole-Suite fence: %+v %v", result, failures[i])
		}
	}
	if winner == -1 {
		t.Fatal("no valid promotion committed")
	}
	snapshot, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil {
		t.Fatal(err)
	}
	effect, _ := results[winner].Decision.Effect()
	if snapshot.Canonical.Version().ID() != effect.Version().ID() || pgCount(t, pool, "promotions") != 1 || pgCount(t, pool, "suite_versions") != 1 {
		t.Fatal("winning version lost or competing effects persisted")
	}
}

func TestPostgresPromotionRollsBackSQLAndCommitFailures(t *testing.T) {
	for _, stage := range []string{"audit statement", "deferred commit"} {
		t.Run(stage, func(t *testing.T) {
			pool, store, _ := newGovernanceDatabase(t)
			fixture := pgGovernanceFixture(t)
			pgInitializeAndApprove(t, store, fixture)
			statement := `CREATE FUNCTION fail_effect() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit SQL failure'; END $$; CREATE TRIGGER fail_effect BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_effect()`
			if stage == "deferred commit" {
				statement = `CREATE FUNCTION fail_effect() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER fail_effect BEFORE INSERT ON publication_intents FOR EACH ROW EXECUTE FUNCTION fail_effect()`
			}
			schemaExec(t, pool, statement)
			result, err := governance.Promote(t.Context(), store, fixture.request)
			if err == nil || result.Committed {
				t.Fatalf("failed transaction exposed success: %+v %v", result, err)
			}
			snapshot, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Fence.Revision != 1 {
				t.Fatal("failed transaction advanced authority")
			}
			if _, present := snapshot.Canonical.Suite().CurrentVersionID(); present {
				t.Fatal("failed transaction changed pointer")
			}
			for table, want := range map[string]int{"suite_versions": 0, "promotions": 0, "operation_receipts": 1, "audit_events": 1, "publication_intents": 0, "consent_acknowledgments": 1} {
				if got := pgCount(t, pool, table); got != want {
					t.Fatalf("rollback left partial %s=%d want%d", table, got, want)
				}
			}
			table := "audit_events"
			if stage == "deferred commit" {
				table = "publication_intents"
			}
			schemaExec(t, pool, "DROP TRIGGER fail_effect ON "+table)
			retry, err := governance.Promote(t.Context(), store, fixture.request)
			if err != nil || !retry.Committed || retry.Duplicate {
				t.Fatalf("rollback reserved operation/version identity: %+v %v", retry, err)
			}
		})
	}
}

type changedConsentView struct {
	governance.Store
	consent contract.Consent
}

func (s changedConsentView) Load(ctx context.Context, r governance.ReadRequest) (governance.Snapshot, error) {
	snapshot, err := s.Store.Load(ctx, r)
	snapshot.Consent = s.consent
	return snapshot, err
}
func TestPostgresRevalidatesRevocationAndPreservesAliasReceipt(t *testing.T) {
	pool, store, _ := newGovernanceDatabase(t)
	fixture := pgGovernanceFixture(t)
	pgInitializeAndApprove(t, store, fixture)
	approved, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil {
		t.Fatal(err)
	}
	revoke := pgValue(contract.NewCommand(contract.CommandInput{OperationID: "revoke", SourceCommandID: "source-revoke", Actor: fixture.owner, Reference: fixture.request.Reference, Carrier: "carrier", Action: contract.RevokeConsent, Order: 2}))
	revoked, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: revoke})
	if err != nil || revoked.Receipt.Result.Outcome() != contract.ConsentRevoked {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	result, err := governance.Promote(t.Context(), changedConsentView{store, approved.Consent}, fixture.request)
	if !errors.Is(err, governance.ErrInvalidRequest) || result.Committed {
		t.Fatalf("caller old approval authorized promotion under current fence: %+v %v", result, err)
	}
	alias := pgValue(contract.NewCommand(contract.CommandInput{OperationID: "alias", SourceCommandID: "source-approve", Actor: fixture.owner, Reference: fixture.request.Reference, Carrier: "changed-carrier", Action: contract.RevokeConsent, Order: 99}))
	aliased, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: alias})
	if err != nil || !aliased.Duplicate || aliased.Receipt.Result.Outcome() != contract.ConsentApproved || !aliased.Receipt.CurrentApprovalEligible {
		t.Fatalf("alias reevaluated historical outcome: %+v %v", aliased, err)
	}
	current, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil || current.Fence.Revision != 3 || current.Consent.HasApproval(current.Proposal, current.Policy) {
		t.Fatalf("alias changed current revocation: %+v %v", current, err)
	}
	for table, want := range map[string]int{"operation_receipts": 3, "consent_sources": 2, "audit_events": 2, "consent_acknowledgments": 2} {
		if got := pgCount(t, pool, table); got != want {
			t.Fatalf("alias duplicated %s: %d want%d", table, got, want)
		}
	}
}

func TestPostgresRejectsReceiptRowIdentityMismatch(t *testing.T) {
	pool, store, _ := newGovernanceDatabase(t)
	fixture := pgGovernanceFixture(t)
	pgInitializeAndApprove(t, store, fixture)
	schemaExec(t, pool, `INSERT INTO operation_receipts(operation_id,project_id,suite_id,kind,receipt_payload) SELECT 'forged','project','suite',kind,receipt_payload FROM operation_receipts WHERE operation_id='approve'`)
	command := pgValue(contract.NewCommand(contract.CommandInput{OperationID: "forged", SourceCommandID: "source-approve", Actor: fixture.owner, Reference: fixture.request.Reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Fatalf("unbound copied receipt accepted as operation: %v", err)
	}
}

func TestPostgresContentAvailabilityPreventsPromotionLoadAndReplay(t *testing.T) {
	pool, store, verifier := newGovernanceDatabase(t)
	fixture := pgGovernanceFixture(t)
	pgInitializeAndApprove(t, store, fixture)
	verifier.absent = fixture.request.Proposed.Manifest().Entries()[0].Content
	if result, err := governance.Promote(t.Context(), store, fixture.request); !errors.Is(err, os.ErrNotExist) || result.Committed {
		t.Fatalf("missing proposed bytes promoted: %+v %v", result, err)
	}
	if pgCount(t, pool, "promotions") != 0 {
		t.Fatal("unavailable bytes created history")
	}
	verifier.absent = artifact.Digest{}
	if result, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !result.Committed {
		t.Fatalf("valid bytes setup: %+v %v", result, err)
	}
	verifier.absent = fixture.request.Proposed.Manifest().Entries()[0].Content
	if _, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical read ignored missing bytes: %v", err)
	}
	if result, err := governance.Promote(t.Context(), store, fixture.request); !errors.Is(err, os.ErrNotExist) || result.Committed {
		t.Fatalf("historical replay ignored original missing bytes: %+v %v", result, err)
	}
}

func pgNextProposal(t *testing.T, fixture governanceFixture) (governanceFixture, contract.Command, governance.PromoteRequest) {
	t.Helper()
	reference := fixture.request.Reference
	reference.ProposalID = "second"
	reference.RevisionID = "second-r1"
	manifest := pgValue(artifact.NewManifest([]artifact.Entry{{Path: "tests/second.txt", Content: artifact.Hash([]byte("second content"))}}))
	protected := pgValue(contract.NewProtectedContract(manifest, fixture.request.Proposed.ScopeDigest(), fixture.request.Proposed.CoveredInputs()))
	binding := pgValue(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: "v1", Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs()}))
	proposal := pgValue(contract.NewProposal(pgValue(contract.NewProposalRevision(binding, "second-origin", "second-carrier"))))
	consent := pgValue(contract.NewConsent("project", "suite", "second"))
	evidence := pgValue(contract.NewIntegrityEvidence("checker", "second-integrated", binding, contract.IntegrityPassed))
	assessment := pgValue(contract.AssessIntegrity("second-integrated", binding, &evidence))
	fixture.authority.Proposals = append(fixture.authority.Proposals, postgres.TrustedProposal{Proposal: proposal, Consent: consent, Assessments: []contract.IntegrityAssessment{assessment}})
	fixture.authority.Scheduling = pgValue(fixture.authority.Scheduling.Admit(proposal, true))
	command := pgValue(contract.NewCommand(contract.CommandInput{OperationID: "second-approve", SourceCommandID: "second-source-approve", Actor: fixture.owner, Reference: reference, Carrier: "second-carrier", Action: contract.ApproveConsent, Order: 1}))
	request := fixture.request
	request.OperationID = "second-promote"
	request.Reference = reference
	request.Carrier = "second-carrier"
	request.Proposed = protected
	request.AssessmentSource = "second-integrated"
	request.Integration = pgValue(contract.NewIntegration("project", "main", "second-integrated", "second-carrier", contract.IntegrationMergedChange))
	request.NewVersionID = "v2"
	return fixture, command, request
}
func TestPostgresPromotionsTranslateUniqueConflicts(t *testing.T) {
	for _, identity := range []string{"version", "exact reference"} {
		t.Run(identity, func(t *testing.T) {
			pool, store, _ := newGovernanceDatabase(t)
			fixture, command, request := pgNextProposal(t, pgGovernanceFixture(t))
			pgInitializeAndApprove(t, store, fixture)
			if result, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !result.Committed {
				t.Fatalf("first promotion: %+v %v", result, err)
			}
			if result, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil || !result.Committed {
				t.Fatalf("second approval: %+v %v", result, err)
			}
			body := `NEW.proposal_id='proposal'; NEW.proposal_revision_id='r1';`
			if identity == "version" {
				body = `NEW.version_id='v1'; NEW.expected_version_id=NULL;`
			}
			schemaExec(t, pool, `CREATE FUNCTION conflict_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` RETURN NEW; END $$; CREATE TRIGGER conflict_identity BEFORE INSERT ON promotions FOR EACH ROW EXECUTE FUNCTION conflict_identity()`)
			result, err := governance.Promote(t.Context(), store, request)
			if !errors.Is(err, governance.ErrVersionConflict) || result.Committed {
				t.Fatalf("real duplicate %s lost stable version conflict: %+v %v", identity, result, err)
			}
			if pgCount(t, pool, "suite_versions") != 1 || pgCount(t, pool, "promotions") != 1 || pgCount(t, pool, "operation_receipts") != 3 {
				t.Fatal("unique conflict left staged second effects")
			}
		})
	}
}

func TestPostgresGovernanceRejectsIncompleteMigrationSequence(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.UpTo(t.Context(), database.url, 1); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := pgValue(postgres.NewStore(pool, &pgVerifier{}))
	fixture := pgGovernanceFixture(t)
	if err := store.InitializeTrusted(t.Context(), fixture.authority); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("migration1 accepted authority write without immutable/revision guards: %v", err)
	}
	if pgCount(t, pool, "suites") != 0 {
		t.Fatal("incomplete schema authority write left effects")
	}
	if _, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference}); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("incomplete schema load: %v", err)
	}
	if err := store.CommitConsent(t.Context(), governance.AuthorityFence{ProjectID: "project", SuiteID: "suite"}, governance.ConsentWrite{Command: fixture.command}); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("incomplete schema consent: %v", err)
	}
	if err := store.CommitPromotion(t.Context(), governance.AuthorityFence{ProjectID: "project", SuiteID: "suite"}, governance.PromotionWrite{Receipt: governance.PromotionReceipt{Identity: governance.PromotionIdentity{Kind: governance.OperationPromote, Request: fixture.request}}}); !errors.Is(err, postgres.ErrSchemaNotReady) {
		t.Fatalf("incomplete schema promotion: %v", err)
	}
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatalf("completed supported sequence remained unavailable: %v", err)
	}
}

func TestPostgresTrustedInitializationRequiresCompleteScopedFacts(t *testing.T) {
	for _,part:=range []string{"absent consent","unknown scheduled proposal","unknown assessed proposal"}{t.Run(part,func(t *testing.T){pool,store,_:=newGovernanceDatabase(t);fixture:=pgGovernanceFixture(t)
		switch part{case "absent consent":fixture.authority.Proposals[0].Consent=contract.Consent{}
		case "unknown scheduled proposal":fixture.authority.Proposals=nil
		case "unknown assessed proposal":reference:=fixture.request.Reference;reference.ProposalID="unknown";binding:=pgValue(contract.NewApprovalBinding(contract.BindingInput{Reference:reference,Manifest:fixture.request.Proposed.Manifest().Digest(),Scope:fixture.request.Proposed.ScopeDigest(),PolicyRevision:"policy",CoveredInputs:fixture.request.Proposed.CoveredInputs()}));fixture.authority.Proposals[0].Assessments=[]contract.IntegrityAssessment{pgValue(contract.AssessIntegrity("integrated",binding,nil))}}
		if err:=store.InitializeTrusted(t.Context(),fixture.authority);!errors.Is(err,governance.ErrInvalidRequest){t.Fatalf("incomplete trusted facts accepted (%s): %v",part,err)};if pgCount(t,pool,"suites")!=0{t.Fatal("invalid initialization left authority")}
	})}
}

type pgVerifier struct{ absent artifact.Digest }

func (v *pgVerifier) Verify(_ context.Context, d artifact.Digest) error {
	if d == v.absent {
		return os.ErrNotExist
	}
	return nil
}

type governanceFixture struct {
	authority postgres.TrustedAuthority
	owner     contract.Principal
	command   contract.Command
	request   governance.PromoteRequest
}

func pgGovernanceFixture(t *testing.T) governanceFixture {
	t.Helper()
	owner := pgValue(contract.NewPrincipal("owner", contract.Human))
	policy := pgValue(contract.NewPolicy("project", "policy", owner))
	manifest := pgValue(artifact.NewManifest([]artifact.Entry{{Path: "tests/test.txt", Content: artifact.Hash([]byte("protected"))}}))
	protected := pgValue(contract.NewProtectedContract(manifest, artifact.Hash([]byte("scope")), map[string]string{"runner": "v1"}))
	reference := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}
	binding := pgValue(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs()}))
	proposal := pgValue(contract.NewProposal(pgValue(contract.NewProposalRevision(binding, "origin", "carrier"))))
	consent := pgValue(contract.NewConsent("project", "suite", "proposal"))
	schedule := pgValue(contract.NewSchedule("project", "suite"))
	schedule = pgValue(schedule.Admit(proposal, true))
	canonical := pgValue(contract.NewCanonicalSnapshot(pgValue(contract.NewSuite("project", "suite", "", 0)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	evidence := pgValue(contract.NewIntegrityEvidence("checker", "integrated", binding, contract.IntegrityPassed))
	assessment := pgValue(contract.AssessIntegrity("integrated", binding, &evidence))
	command := pgValue(contract.NewCommand(contract.CommandInput{OperationID: "approve", SourceCommandID: "source-approve", Actor: owner, Reference: reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	request := governance.PromoteRequest{OperationID: "promote", Reference: reference, Carrier: "carrier", Proposed: protected, AssessmentSource: "integrated", Integration: pgValue(contract.NewIntegration("project", "main", "integrated", "carrier", contract.IntegrationMergedChange)), NewVersionID: "v1", RecordedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	return governanceFixture{postgres.TrustedAuthority{Canonical: canonical, Policy: policy, Scheduling: schedule, Target: "main", Proposals: []postgres.TrustedProposal{{Proposal: proposal, Consent: consent, Assessments: []contract.IntegrityAssessment{assessment}}}}, owner, command, request}
}
func newGovernanceDatabase(t *testing.T) (*pgxpool.Pool, *postgres.Store, *pgVerifier) {
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
	verifier := &pgVerifier{}
	store, err := postgres.NewStore(pool, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, verifier
}
func pgCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresGovernanceCommitsAndReplaysExactOutcomes(t *testing.T) {
	pool, store, _ := newGovernanceDatabase(t)
	fixture := pgGovernanceFixture(t)
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatalf("initialize valid unbaselined authority: %v", err)
	}
	initial, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference, AssessmentSource: "integrated"})
	if err != nil || initial.Fence.Revision != 0 || !reflect.DeepEqual(initial.Proposal, fixture.authority.Proposals[0].Proposal) {
		t.Fatalf("load coherent initial authority: %+v %v", initial, err)
	}
	approved, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command})
	if err != nil || !approved.Committed || approved.Receipt.Result.Outcome() != contract.ConsentApproved {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	promoted, err := governance.Promote(t.Context(), store, fixture.request)
	if err != nil || !promoted.Committed || promoted.Decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("promote: %+v %v", promoted, err)
	}
	retry := fixture.request
	retry.RecordedAt = retry.RecordedAt.Add(time.Hour)
	replayed, err := governance.Promote(t.Context(), store, retry)
	if err != nil || !replayed.Committed || !replayed.Duplicate || !reflect.DeepEqual(replayed.Decision, promoted.Decision) {
		t.Fatalf("replay original exact outcome: %+v %v", replayed, err)
	}
	current, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference, HistoricalVersionID: "v1"})
	if err != nil || current.Canonical.Version().ID() != "v1" || current.Fence.Revision != 2 || current.History.Version().ID() != "v1" {
		t.Fatalf("persisted pointer and history: %+v %v", current, err)
	}
	for table, want := range map[string]int{"suite_versions": 1, "promotions": 1, "operation_receipts": 2, "audit_events": 2, "publication_intents": 1, "consent_acknowledgments": 1, "consent_sources": 1} {
		if got := pgCount(t, pool, table); got != want {
			t.Errorf("%s count=%d want%d", table, got, want)
		}
	}
	retry.NewVersionID = "different-version"
	if _, err := governance.Promote(t.Context(), store, retry); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("changed operation identity adopted original result: %v", err)
	}
}
