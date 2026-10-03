//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func pgValue[T any](value T,err error) T {if err!=nil{panic(err)};return value}
type pgVerifier struct { absent artifact.Digest }
func (v *pgVerifier) Verify(_ context.Context,d artifact.Digest) error {if d==v.absent{return os.ErrNotExist};return nil}
type governanceFixture struct { authority postgres.TrustedAuthority; owner contract.Principal; command contract.Command; request governance.PromoteRequest }
func pgGovernanceFixture(t *testing.T) governanceFixture {
	t.Helper()
	owner:=pgValue(contract.NewPrincipal("owner",contract.Human));policy:=pgValue(contract.NewPolicy("project","policy",owner))
	manifest:=pgValue(artifact.NewManifest([]artifact.Entry{{Path:"tests/test.txt",Content:artifact.Hash([]byte("protected"))}}))
	protected:=pgValue(contract.NewProtectedContract(manifest,artifact.Hash([]byte("scope")),map[string]string{"runner":"v1"}))
	reference:=contract.ProposalReference{ProjectID:"project",SuiteID:"suite",ProposalID:"proposal",RevisionID:"r1"}
	binding:=pgValue(contract.NewApprovalBinding(contract.BindingInput{Reference:reference,Manifest:manifest.Digest(),Scope:protected.ScopeDigest(),PolicyRevision:"policy",CoveredInputs:protected.CoveredInputs()}))
	proposal:=pgValue(contract.NewProposal(pgValue(contract.NewProposalRevision(binding,"origin","carrier"))))
	consent:=pgValue(contract.NewConsent("project","suite","proposal"));schedule:=pgValue(contract.NewSchedule("project","suite"));schedule=pgValue(schedule.Admit(proposal,true))
	canonical:=pgValue(contract.NewCanonicalSnapshot(pgValue(contract.NewSuite("project","suite","",0)),contract.SuiteVersion{},contract.ProtectedContract{},contract.PromotionRecord{}))
	evidence:=pgValue(contract.NewIntegrityEvidence("checker","integrated",binding,contract.IntegrityPassed));assessment:=pgValue(contract.AssessIntegrity("integrated",binding,&evidence))
	command:=pgValue(contract.NewCommand(contract.CommandInput{OperationID:"approve",SourceCommandID:"source-approve",Actor:owner,Reference:reference,Carrier:"carrier",Action:contract.ApproveConsent,Order:1}))
	request:=governance.PromoteRequest{OperationID:"promote",Reference:reference,Carrier:"carrier",Proposed:protected,AssessmentSource:"integrated",Integration:pgValue(contract.NewIntegration("project","main","integrated","carrier",contract.IntegrationMergedChange)),NewVersionID:"v1",RecordedAt:time.Date(2026,10,2,12,0,0,0,time.UTC)}
	return governanceFixture{postgres.TrustedAuthority{Canonical:canonical,Policy:policy,Scheduling:schedule,Target:"main",Proposals:[]postgres.TrustedProposal{{Proposal:proposal,Consent:consent,Assessments:[]contract.IntegrityAssessment{assessment}}}},owner,command,request}
}
func newGovernanceDatabase(t *testing.T) (*pgxpool.Pool,*postgres.Store,*pgVerifier) {
	t.Helper();database:=newSchemaDatabase(t);if err:=migrations.Up(t.Context(),database.url);err!=nil{t.Fatal(err)}
	pool,err:=pgxpool.New(t.Context(),database.url);if err!=nil{t.Fatal(err)};t.Cleanup(pool.Close)
	verifier:=&pgVerifier{};store,err:=postgres.NewStore(pool,verifier);if err!=nil{t.Fatal(err)};return pool,store,verifier
}
func pgCount(t *testing.T,pool *pgxpool.Pool,table string) int {t.Helper();var count int;if err:=pool.QueryRow(t.Context(),"SELECT count(*) FROM "+table).Scan(&count);err!=nil{t.Fatal(err)};return count}

func TestPostgresGovernanceCommitsAndReplaysExactOutcomes(t *testing.T) {
	pool,store,_:=newGovernanceDatabase(t);fixture:=pgGovernanceFixture(t)
	if err:=store.InitializeTrusted(t.Context(),fixture.authority);err!=nil{t.Fatalf("initialize valid unbaselined authority: %v",err)}
	initial,err:=store.Load(t.Context(),governance.ReadRequest{Reference:fixture.request.Reference,AssessmentSource:"integrated"});if err!=nil||initial.Fence.Revision!=0||!reflect.DeepEqual(initial.Proposal,fixture.authority.Proposals[0].Proposal){t.Fatalf("load coherent initial authority: %+v %v",initial,err)}
	approved,err:=governance.ProcessConsent(t.Context(),store,governance.ConsentRequest{Command:fixture.command});if err!=nil||!approved.Committed||approved.Receipt.Result.Outcome()!=contract.ConsentApproved{t.Fatalf("approve: %+v %v",approved,err)}
	promoted,err:=governance.Promote(t.Context(),store,fixture.request);if err!=nil||!promoted.Committed||promoted.Decision.Outcome()!=contract.PromotionProposed{t.Fatalf("promote: %+v %v",promoted,err)}
	retry:=fixture.request;retry.RecordedAt=retry.RecordedAt.Add(time.Hour)
	replayed,err:=governance.Promote(t.Context(),store,retry);if err!=nil||!replayed.Committed||!replayed.Duplicate||!reflect.DeepEqual(replayed.Decision,promoted.Decision){t.Fatalf("replay original exact outcome: %+v %v",replayed,err)}
	current,err:=store.Load(t.Context(),governance.ReadRequest{Reference:fixture.request.Reference,HistoricalVersionID:"v1"});if err!=nil||current.Canonical.Version().ID()!="v1"||current.Fence.Revision!=2||current.History.Version().ID()!="v1"{t.Fatalf("persisted pointer and history: %+v %v",current,err)}
	for table,want:=range map[string]int{"suite_versions":1,"promotions":1,"operation_receipts":2,"audit_events":2,"publication_intents":1,"consent_acknowledgments":1,"consent_sources":1}{if got:=pgCount(t,pool,table);got!=want{t.Errorf("%s count=%d want%d",table,got,want)}}
	retry.NewVersionID="different-version";if _,err:=governance.Promote(t.Context(),store,retry);!errors.Is(err,governance.ErrOperationConflict){t.Fatalf("changed operation identity adopted original result: %v",err)}
}
