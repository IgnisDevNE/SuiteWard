package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func approvedPromotionFixture(t *testing.T) storeFixture {
	t.Helper()
	f := newStoreFixture(t)
	f.snapshot.Consent = f.consentWrite(t).Consent
	return f
}

// changedPromotionLoad exercises defensive handling at the Store boundary,
// while all state and commit behavior still use the shared reference store.
type changedPromotionLoad struct {
	Store
	change func(*Snapshot)
}
func (s changedPromotionLoad) Load(ctx context.Context, request ReadRequest) (Snapshot,error) {
	value,err:=s.Store.Load(ctx,request)
	if err==nil {s.change(&value)}
	return value,err
}

func requireZeroPromotionResult(t *testing.T, result PromoteResult) {
	t.Helper()
	if !reflect.DeepEqual(result,PromoteResult{}) {t.Fatalf("error exposed an outcome as success: %+v",result)}
}

func TestPromoteRejectsMalformedRequestBeforeLoading(t *testing.T) {
	for name,change:=range map[string]func(*PromoteRequest){
		"operation":func(r *PromoteRequest){r.OperationID=""},
		"blank operation":func(r *PromoteRequest){r.OperationID="\t"},
		"project":func(r *PromoteRequest){r.Reference.ProjectID=""},
		"suite":func(r *PromoteRequest){r.Reference.SuiteID=" "},
		"proposal":func(r *PromoteRequest){r.Reference.ProposalID=""},
		"revision":func(r *PromoteRequest){r.Reference.RevisionID="\n"},
		"carrier":func(r *PromoteRequest){r.Carrier=""},
		"blank carrier":func(r *PromoteRequest){r.Carrier=" \t"},
		"proposed":func(r *PromoteRequest){r.Proposed=contract.ProtectedContract{}},
		"assessment source":func(r *PromoteRequest){r.AssessmentSource=""},
		"blank assessment source":func(r *PromoteRequest){r.AssessmentSource="\n"},
	}{t.Run(name,func(t *testing.T){f:=approvedPromotionFixture(t);store:=newReferenceStore(t,f.snapshot);store.failAt="load";change(&f.request);result,err:=Promote(context.Background(),store,f.request);if !errors.Is(err,ErrInvalidRequest){t.Fatalf("malformed request reached authority loading: %v",err)};requireZeroPromotionResult(t,result)})}
}

func TestPromoteRejectsMalformedLoadedAuthority(t *testing.T) {
	for name,change:=range map[string]func(*Snapshot){
		"canonical":func(s *Snapshot){s.Canonical=contract.CanonicalSnapshot{}},
		"fence project":func(s *Snapshot){s.Fence.ProjectID="other"},
		"fence suite":func(s *Snapshot){s.Fence.SuiteID="other"},
		"fence revision":func(s *Snapshot){s.Fence.Revision++},
		"proposal":func(s *Snapshot){s.Proposal=contract.Proposal{}},
		"policy":func(s *Snapshot){s.Policy=contract.Policy{}},
		"foreign policy":func(s *Snapshot){owner,err:=contract.NewPrincipal("owner",contract.Human);if err!=nil{t.Fatal(err)};s.Policy,err=contract.NewPolicy("other","policy",owner);if err!=nil{t.Fatal(err)}},
		"schedule":func(s *Snapshot){s.Scheduling=contract.Schedule{}},
		"foreign schedule":func(s *Snapshot){value,err:=contract.NewSchedule("project","other");if err!=nil{t.Fatal(err)};s.Scheduling=value},
		"target":func(s *Snapshot){s.Target=""},
		"blank target":func(s *Snapshot){s.Target="\n"},
	}{t.Run(name,func(t *testing.T){f:=approvedPromotionFixture(t);store:=newReferenceStore(t,f.snapshot);before:=store.inspect();result,err:=Promote(context.Background(),changedPromotionLoad{Store:store,change:change},f.request);if !errors.Is(err,ErrInvalidSnapshot){t.Fatalf("malformed stored authority accepted: %v",err)};requireZeroPromotionResult(t,result);if !reflect.DeepEqual(before,store.inspect()){t.Fatal("invalid snapshot caused a write")}})}
}

func TestPromoteProvisionalOutcomesNeverCommit(t *testing.T) {
	f:=newStoreFixture(t)
	store:=newReferenceStore(t,f.snapshot)
	before:=store.inspect()
	result,err:=Promote(context.Background(),store,f.request)
	if err!=nil||result.Committed||result.Duplicate||result.Decision.Outcome()!=contract.PromotionBlocked||result.Decision.Reason()!=contract.PromotionReasonApprovalMissing{t.Fatalf("missing consent was not provisional: %+v %v",result,err)}
	if !reflect.DeepEqual(before,store.inspect()){t.Fatal("blocked proposal changed authority")}
	f=approvedPromotionFixture(t)
	store=newReferenceStore(t,f.snapshot)
	if result,err:=Promote(context.Background(),store,f.request);err!=nil||!result.Committed{t.Fatalf("setup promotion failed: %+v %v",result,err)}
	before=store.inspect()
	f.request.OperationID="no-change"
	f.request.NewVersionID=""
	f.request.RecordedAt=time.Time{}
	result,err=Promote(context.Background(),store,f.request)
	if err!=nil||result.Committed||result.Duplicate||result.Decision.Outcome()!=contract.PromotionNoChange{t.Fatalf("unchanged contract was not provisional: %+v %v",result,err)}
	if !reflect.DeepEqual(before,store.inspect()){t.Fatal("no-change reserved an operation or advanced queue")}
}

func TestPromoteFailureNeverReportsOrExposesPartialCommit(t *testing.T) {
	for _,point:=range []string{"load","version","pointer","promotion","receipt","audit","schedule","publication"}{
		t.Run(point,func(t *testing.T){f:=approvedPromotionFixture(t);store:=newReferenceStore(t,f.snapshot);before:=store.inspect();store.failAt=point;result,err:=Promote(context.Background(),store,f.request);if !errors.Is(err,errReferenceFailure){t.Fatalf("injected %s failure not returned: %v",point,err)};requireZeroPromotionResult(t,result);if !reflect.DeepEqual(before,store.inspect()){t.Fatalf("failure at %s exposed partial authority or publication",point)};store.failAt="";result,err=Promote(context.Background(),store,f.request);if err!=nil||!result.Committed||result.Duplicate{t.Fatalf("retry after rollback failed: %+v %v",result,err)}})
	}
}

func TestPromoteCommitsStoredAuthorityAndQueueTogether(t *testing.T) {
	f := approvedPromotionFixture(t)
	store := newReferenceStore(t, f.snapshot)
	result, err := Promote(context.Background(), store, f.request)
	if err != nil || !result.Committed || result.Duplicate || result.Decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("promotion was not committed: committed=%v duplicate=%v outcome=%v err=%v", result.Committed, result.Duplicate, result.Decision.Outcome(), err)
	}
	state := store.inspect()
	effect, present := result.Decision.Effect()
	if !present || effect.Version().ID() != f.request.NewVersionID || state.canonical.Version().ID() != effect.Version().ID() || state.canonical.Suite().Revision() != f.snapshot.Fence.Revision+1 {
		t.Fatal("committed result and canonical version disagree")
	}
	if len(state.versions) != 1 || len(state.promotions) != 1 || len(state.operations) != 1 || len(state.audits) != 1 || len(state.publications) != 1 {
		t.Fatal("promotion did not persist all required immutable records and publication intent")
	}
	receipt := state.operations[f.request.OperationID]
	if receipt.Kind != OperationPromote || receipt.Promotion.Identity.Kind != OperationPromote || receipt.Promotion.Identity.Request.Reference != f.request.Reference || !receipt.Promotion.Identity.Binding.Equal(f.snapshot.Proposal.Current().Binding()) {
		t.Fatal("committed receipt lost exact operation identity and binding")
	}
	entries := state.scheduling.Entries()
	if len(entries) != 1 || entries[0].State() != contract.SchedulePromoted || state.scheduling.Generation() != f.snapshot.Scheduling.Generation()+1 {
		t.Fatal("canonical commit did not release the completed scheduling entry atomically")
	}
	if f.snapshot.Canonical.Version().ID() != "" || f.snapshot.Scheduling.Entries()[0].State() != contract.ScheduleActive {
		t.Fatal("application mutated its supplied immutable authority values")
	}
}
