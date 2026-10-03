package contract

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func checkpointValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func checkpointFixture(t *testing.T) StateCheckpoint {
	t.Helper()
	owner := checkpointValue(NewPrincipal("owner", Human))
	policy := checkpointValue(NewPolicy("project", "policy", owner))
	manifest := checkpointValue(artifact.NewManifest([]artifact.Entry{{Path: "tests/test.txt", Content: artifact.Hash([]byte("protected"))}}))
	protected := checkpointValue(NewProtectedContract(manifest, artifact.Hash([]byte("scope")), map[string]string{"runner": "v1"}))
	reference := ProposalReference{"project", "suite", "proposal", "r1"}
	binding := checkpointValue(NewApprovalBinding(BindingInput{Reference: reference, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: policy.RevisionID(), CoveredInputs: protected.CoveredInputs()}))
	revision := checkpointValue(NewProposalRevision(binding, "origin", "carrier"))
	proposal := checkpointValue(NewProposal(revision))
	consent := checkpointValue(NewConsent("project", "suite", "proposal"))
	command := checkpointValue(NewCommand(CommandInput{OperationID: "approve", SourceCommandID: "source-approve", Actor: owner, Reference: reference, Carrier: "carrier", Action: ApproveConsent, Order: 1}))
	consent, result, err := consent.Apply(proposal, policy, command)
	if err != nil {
		t.Fatal(err)
	}
	alias := checkpointValue(NewCommand(CommandInput{OperationID: "alias", SourceCommandID: "source-approve", Actor: owner, Reference: reference, Carrier: "carrier", Action: ApproveConsent, Order: 1}))
	consent, _, err = consent.Apply(proposal, policy, alias)
	if err != nil {
		t.Fatal(err)
	}
	schedule := checkpointValue(NewSchedule("project", "suite"))
	schedule = checkpointValue(schedule.Admit(proposal, true))
	suite := checkpointValue(NewSuite("project", "suite", "", 4))
	canonical := checkpointValue(NewCanonicalSnapshot(suite, SuiteVersion{}, ProtectedContract{}, PromotionRecord{}))
	evidence := checkpointValue(NewIntegrityEvidence("service", "integrated", binding, IntegrityPassed))
	assessment := checkpointValue(AssessIntegrity("integrated", binding, &evidence))
	integration := checkpointValue(NewIntegration("project", "main", "integrated", "carrier", IntegrationMergedChange))
	decision := checkpointValue(DecidePromotion(PromotionInput{Context: PromotionContext{Canonical: canonical, Proposed: protected, Proposal: proposal, Reference: reference, Carrier: "carrier", Policy: policy, Consent: consent, Assessment: assessment, Scheduling: schedule, ExpectedStateRevision: 4, ExpectedSchedulingGeneration: schedule.Generation()}, Integration: integration, Target: "main", OperationID: "promote", NewVersionID: "v1", RecordedAt: time.Date(2026, 10, 2, 12, 0, 0, 123, time.UTC)}))
	effect, ok := decision.Effect()
	if !ok {
		t.Fatal("fixture promotion has no effect")
	}
	history := checkpointValue(NewHistoricalCanonical(effect.Version(), effect.Promotion()))
	canonical = checkpointValue(NewCanonicalSnapshot(effect.Suite(), effect.Version(), protected, effect.Promotion()))
	// Preserve an old active approval after both proposal and policy have moved.
	reference.RevisionID = "r2"
	binding2 := checkpointValue(NewApprovalBinding(BindingInput{Reference: reference, ExpectedCanonical: "v1", Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy-2", CoveredInputs: protected.CoveredInputs()}))
	proposal = checkpointValue(proposal.Revise(checkpointValue(NewProposalRevision(binding2, "origin-2", "carrier"))))
	policy2 := checkpointValue(NewPolicy("project", "policy-2", checkpointValue(NewPrincipal("new-owner", Human))))
	secondRef := ProposalReference{"project", "suite", "second", "r1"}
	secondBinding := checkpointValue(NewApprovalBinding(BindingInput{Reference: secondRef, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs()}))
	second := checkpointValue(NewProposal(checkpointValue(NewProposalRevision(secondBinding, "second-origin", "second-carrier"))))
	schedule = checkpointValue(schedule.Admit(second, true))
	priority := checkpointValue(NewPriorityCommand(PriorityCommandInput{OperationID: "priority", SourceCommandID: "priority-source", Actor: owner, ProjectID: "project", SuiteID: "suite", ProposalID: "second", Carrier: "second-carrier", Order: 3}))
	schedule, _, err = schedule.RequestPriority(policy, priority)
	if err != nil {
		t.Fatal(err)
	}
	priorityAlias := checkpointValue(NewPriorityCommand(PriorityCommandInput{OperationID: "priority-alias", SourceCommandID: "priority-source", Actor: owner, ProjectID: "project", SuiteID: "suite", ProposalID: "second", Carrier: "second-carrier", Order: 3}))
	schedule, _, err = schedule.RequestPriority(policy, priorityAlias)
	if err != nil {
		t.Fatal(err)
	}
	return StateCheckpoint{Canonical: canonical, Proposal: proposal, Policy: policy2, Consent: consent, Scheduling: schedule, Assessment: assessment, Integration: integration, History: history, PromotionDecision: decision, CommandResult: result, Protected: protected, Command: alias}
}

func TestStateCheckpointPreservesHistoricalFactsAndAliases(t *testing.T) {
	want := checkpointFixture(t)
	encoded, err := EncodeStateCheckpoint(want)
	if err != nil {
		t.Fatalf("encode usable historical state: %v", err)
	}
	got, err := RestoreStateCheckpoint(encoded)
	if err != nil {
		t.Fatalf("restore usable historical state: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restoration changed immutable facts: got=%+v want=%+v", got, want)
	}
	if got.Consent.HasApproval(got.Proposal, got.Policy) {
		t.Fatal("old approval became eligible in the new context")
	}
	conflict := checkpointValue(NewCommand(CommandInput{OperationID: "alias", SourceCommandID: "different-source", Actor: want.Command.Actor(), Reference: want.Command.Reference(), Carrier: "carrier", Action: RevokeConsent, Order: 9}))
	_, result, err := got.Consent.Apply(got.Proposal, got.Policy, conflict)
	if err != nil || result.Reason() != ConsentReasonCommandConflict {
		t.Fatalf("consent alias lost original identity: %+v %v", result, err)
	}
	priorityConflict := checkpointValue(NewPriorityCommand(PriorityCommandInput{OperationID: "priority-alias", SourceCommandID: "different-priority-source", Actor: want.Command.Actor(), ProjectID: "project", SuiteID: "suite", ProposalID: "second", Carrier: "second-carrier", Order: 9}))
	_, priorityResult, err := got.Scheduling.RequestPriority(got.Policy, priorityConflict)
	if err != nil || priorityResult.Reason() != PriorityReasonCommandConflict {
		t.Fatalf("schedule alias lost original identity: %+v %v", priorityResult, err)
	}
}

func TestStateCheckpointRejectsMalformedVersionAndJSON(t *testing.T) {
	for _, input := range []string{"", "null", "{}", `{"version":99}`, `{"version":1,"unknown":true}`, `{"version":1} {}`, `{"version":1,"version":1}`} {
		if _, err := RestoreStateCheckpoint([]byte(input)); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("accepted malformed checkpoint %q: %v", input, err)
		}
	}
}

func checkpointJSONChange(t *testing.T, encoded []byte, path string, value any) []byte {
	t.Helper()
	var root any
	if err:=json.Unmarshal(encoded,&root);err!=nil{t.Fatal(err)}
	parts:=strings.Split(path,"/");node:=root
	for _,part:=range parts[:len(parts)-1]{switch value:=node.(type){case map[string]any:node=value[part];case []any:index,err:=strconv.Atoi(part);if err!=nil{t.Fatal(err)};node=value[index];default:t.Fatalf("invalid fixture path %s",path)}}
	key:=parts[len(parts)-1]
	switch target:=node.(type){case map[string]any:target[key]=value;case []any:index,err:=strconv.Atoi(key);if err!=nil{t.Fatal(err)};target[index]=value;default:t.Fatalf("invalid fixture path %s",path)}
	changed,err:=json.Marshal(root);if err!=nil{t.Fatal(err)};return changed
}

func TestStateCheckpointRejectsCorruptPersistedFacts(t *testing.T) {
	encoded,err:=EncodeStateCheckpoint(checkpointFixture(t));if err!=nil{t.Fatal(err)}
	invalid:=map[string]any{
		"canonical/Suite/Project":"", "canonical/Version/Manifest/Digest":"", "canonical/Protected/Scope":"", "canonical/Record/Binding/Manifest":"",
		"history/Version/Manifest/Digest":"", "history/Record/Binding/Manifest":"",
		"protected/Manifest/Entries/0/Content":"", "protected/Manifest/Entries/0/Path":"", "protected/Manifest/Digest":"", "protected/Scope":"", "protected/Covered/runner":"",
		"proposal/Revisions":[]any{}, "proposal/Revisions/0/Binding/Manifest":"", "proposal/Revisions/0/Binding/Scope":"", "proposal/Revisions/0/Origin":"", "proposal/Revisions/1/Binding/Reference/ProposalID":"foreign", "proposal/Revisions/1/Binding/Reference/RevisionID":"r1",
		"policy/Owner":"", "policy/Revision":"", "command/Actor/Kind":0, "command/OperationID":"",
		"result/Command/Actor/Kind":0, "result/Outcome":0, "result/Reason":ConsentReasonUnauthorized,
		"consent/Project":"", "consent/Results/0/Command/Actor/Kind":0, "consent/Results/0/Duplicate":true, "consent/Results/0/Command/Reference/SuiteID":"foreign", "consent/States/0/Actor/Kind":0, "consent/States/0/Binding/Manifest":"", "consent/States/0/Order":9, "consent/States":[]any{}, "consent/Operations":nil, "consent/Operations/approve":"wrong-source", "consent/Operations/extra":"unknown-source",
		"scheduling/Project":"", "scheduling/Generation":0, "scheduling/Entries/0/State":0, "scheduling/Entries/1/State":ScheduleActive,
		"scheduling/Results/0/Command/Actor/Kind":0, "scheduling/Results/0/Outcome":0, "scheduling/Results/0/Reason":PriorityReasonUnauthorized, "scheduling/Results/0/Duplicate":true, "scheduling/Results/0/Command/Project":"foreign", "scheduling/Order":9, "scheduling/Operations":nil, "scheduling/Pending/Actor/Kind":0, "scheduling/Pending/Project":"foreign", "scheduling/Pending/Proposal":"unknown", "scheduling/Pending/OperationID":"wrong-operation",
		"assessment/Binding/Manifest":"", "assessment/Evidence/Binding/Manifest":"", "assessment/Evidence/Emitter":"", "assessment/Source":"", "assessment/Reason":IntegrityReasonFailed,
		"integration/Kind":0,
		"decision/Outcome":0, "decision/Effect":nil, "decision/Effect/Suite/Project":"", "decision/Effect/Version/Manifest/Digest":"", "decision/Effect/Record/Binding/Manifest":"", "decision/Effect/ExpectedState":9,
	}
	for path,value:=range invalid{t.Run(path,func(t *testing.T){if _,err:=RestoreStateCheckpoint(checkpointJSONChange(t,encoded,path,value));!errors.Is(err,ErrInvalidCheckpoint){t.Fatalf("corrupt persisted fact accepted: %v",err)}})}
}

func TestStateCheckpointEmptyAndUnbaselinedValues(t *testing.T) {
	for _,value:=range []StateCheckpoint{{},{Canonical:checkpointValue(NewCanonicalSnapshot(checkpointValue(NewSuite("project","suite","",0)),SuiteVersion{},ProtectedContract{},PromotionRecord{})),Consent:checkpointValue(NewConsent("project","suite","proposal")),Scheduling:checkpointValue(NewSchedule("project","suite"))}}{
		encoded,err:=EncodeStateCheckpoint(value);if err!=nil{t.Fatal(err)};got,err:=RestoreStateCheckpoint(encoded);if err!=nil||!reflect.DeepEqual(got,value){t.Fatalf("empty state changed: %+v %v",got,err)}
	}
}
