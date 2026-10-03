package contract

import (
	"bytes"
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
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(path, "/")
	node := root
	for _, part := range parts[:len(parts)-1] {
		switch value := node.(type) {
		case map[string]any:
			node = value[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil {
				t.Fatal(err)
			}
			node = value[index]
		default:
			t.Fatalf("invalid fixture path %s", path)
		}
	}
	key := parts[len(parts)-1]
	switch target := node.(type) {
	case map[string]any:
		target[key] = value
	case []any:
		index, err := strconv.Atoi(key)
		if err != nil {
			t.Fatal(err)
		}
		target[index] = value
	default:
		t.Fatalf("invalid fixture path %s", path)
	}
	changed, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestStateCheckpointRejectsCorruptPersistedFacts(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(checkpointFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	invalid := map[string]any{
		"canonical/Suite/Project": "", "canonical/Version/Manifest/Digest": "", "canonical/Protected/Scope": "", "canonical/Record/Binding/Manifest": "",
		"history/Version/Manifest/Digest": "", "history/Record/Binding/Manifest": "",
		"protected/Manifest/Entries/0/Content": "", "protected/Manifest/Entries/0/Path": "", "protected/Manifest/Digest": "", "protected/Scope": "", "protected/Covered/runner": "",
		"proposal/Revisions": []any{}, "proposal/Revisions/0/Binding/Manifest": "", "proposal/Revisions/0/Binding/Scope": "", "proposal/Revisions/0/Origin": "", "proposal/Revisions/1/Binding/Reference/ProposalID": "foreign", "proposal/Revisions/1/Binding/Reference/RevisionID": "r1",
		"policy/Owner": "", "policy/Revision": "", "command/Actor/Kind": 0, "command/OperationID": "",
		"result/Command/Actor/Kind": 0, "result/Outcome": 0, "result/Reason": ConsentReasonUnauthorized,
		"consent/Project": "", "consent/Results/0/Command/Actor/Kind": 0, "consent/Results/0/Duplicate": true, "consent/Results/0/Command/Reference/SuiteID": "foreign", "consent/States/0/Actor/Kind": 0, "consent/States/0/Binding/Manifest": "", "consent/States/0/Order": 9, "consent/States": []any{}, "consent/Operations": nil, "consent/Operations/approve": "wrong-source", "consent/Operations/extra": "unknown-source",
		"scheduling/Project": "", "scheduling/Generation": 0, "scheduling/Entries/0/State": 0, "scheduling/Entries/1/State": ScheduleActive,
		"scheduling/Results/0/Command/Actor/Kind": 0, "scheduling/Results/0/Outcome": 0, "scheduling/Results/0/Reason": PriorityReasonUnauthorized, "scheduling/Results/0/Duplicate": true, "scheduling/Results/0/Command/Project": "foreign", "scheduling/Order": 9, "scheduling/Operations": nil, "scheduling/Pending/Actor/Kind": 0, "scheduling/Pending/Project": "foreign", "scheduling/Pending/Proposal": "unknown", "scheduling/Pending/OperationID": "wrong-operation",
		"assessment/Binding/Manifest": "", "assessment/Evidence/Binding/Manifest": "", "assessment/Evidence/Emitter": "", "assessment/Source": "", "assessment/Reason": IntegrityReasonFailed,
		"integration/Kind": 0,
		"decision/Outcome": 0, "decision/Effect": nil, "decision/Effect/Suite/Project": "", "decision/Effect/Version/Manifest/Digest": "", "decision/Effect/Record/Binding/Manifest": "", "decision/Effect/ExpectedState": 9,
	}
	for path, value := range invalid {
		t.Run(path, func(t *testing.T) {
			if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, encoded, path, value)); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("corrupt persisted fact accepted: %v", err)
			}
		})
	}
}

func TestStateCheckpointRejectsPairedForeignConsent(t *testing.T) {
	state := checkpointFixture(t)
	state.Consent = checkpointValue(NewConsent("project", "foreign-suite", "proposal"))
	if _, err := EncodeStateCheckpoint(state); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("paired foreign consent accepted: %v", err)
	}
}

func TestStateCheckpointRejectsPairedConsentBindingMismatch(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(checkpointFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]any{"consent/States/0/Binding/Scope": artifact.Hash([]byte("different scope")).String(), "consent/Results/0/Command/Carrier": "different-carrier"} {
		if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, encoded, path, value)); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("paired historical fact mismatch accepted (%s): %v", path, err)
		}
	}
}

func TestStateCheckpointEmptyAndUnbaselinedValues(t *testing.T) {
	for _, value := range []StateCheckpoint{{}, {Canonical: checkpointValue(NewCanonicalSnapshot(checkpointValue(NewSuite("project", "suite", "", 0)), SuiteVersion{}, ProtectedContract{}, PromotionRecord{})), Consent: checkpointValue(NewConsent("project", "suite", "proposal")), Scheduling: checkpointValue(NewSchedule("project", "suite"))}} {
		encoded, err := EncodeStateCheckpoint(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := RestoreStateCheckpoint(encoded)
		if err != nil || !reflect.DeepEqual(got, value) {
			t.Fatalf("empty state changed: %+v %v", got, err)
		}
	}
}

func TestCheckpointPreservesValidEscapedIdentities(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Protected: checkpointFixture(t).Protected})
	if err != nil {
		t.Fatal(err)
	}
	for escaped, want := range map[string]string{
		`"\ud834\udd1e"`: "𝄞",
		`"\ufffd"`:       "�",
		`"\\ud800"`:      `\ud800`,
		`"\u0076\u0031"`: "v1",
		`"x\\y\"z"`:      `x\y"z`,
	} {
		t.Run(escaped, func(t *testing.T) {
			changed := bytes.Replace(encoded, []byte(`"v1"`), []byte(escaped), 1)
			got, err := RestoreStateCheckpoint(changed)
			if err != nil || got.Protected.CoveredInputs()["runner"] != want {
				t.Fatalf("valid escaped identity changed: got %q, want %q, error %v", got.Protected.CoveredInputs()["runner"], want, err)
			}
		})
	}
}

func TestCheckpointRejectsNonrepresentableConsentIdentitiesWithoutMutation(t *testing.T) {
	for _, duplicate := range []bool{true, false} {
		name := "rejected original receipt"
		if duplicate {
			name = "duplicate operation alias"
		}
		t.Run(name, func(t *testing.T) {
			fixture := checkpointFixture(t)
			original := StateCheckpoint{Proposal: fixture.Proposal, Consent: fixture.Consent}
			before, err := EncodeStateCheckpoint(original)
			if err != nil {
				t.Fatal(err)
			}
			invalidID := string([]byte{'i', 0xff})
			input := CommandInput{OperationID: "rejected-operation", SourceCommandID: "rejected-source", Actor: checkpointValue(NewPrincipal(PrincipalID(invalidID), Human)), Reference: fixture.Proposal.Current().Binding().Reference(), Carrier: fixture.Proposal.Current().Carrier(), Action: ApproveConsent, Order: 9}
			if duplicate {
				input.OperationID = OperationID(invalidID)
				input.SourceCommandID = fixture.Command.SourceCommandID()
				input.Actor = fixture.Command.Actor()
				input.Reference = fixture.Command.Reference()
			}
			command := checkpointValue(NewCommand(input))
			next, result, err := fixture.Consent.Apply(fixture.Proposal, fixture.Policy, command)
			if err != nil || result.Duplicate() != duplicate {
				t.Fatalf("constructor-valid consent command did not produce expected history: %+v, %v", result, err)
			}
			if duplicate {
				if result.Command() != fixture.CommandResult.Command() || !reflect.DeepEqual(next.Results(), fixture.Consent.Results()) {
					t.Fatal("duplicate alias changed the original immutable receipt")
				}
			} else if result.Outcome() != ConsentRejected || result.Reason() != ConsentReasonUnauthorized || len(next.Results()) != len(fixture.Consent.Results())+1 {
				t.Fatalf("nonowner command did not retain its rejected receipt: %+v", result)
			}
			if encoded, err := EncodeStateCheckpoint(StateCheckpoint{Proposal: fixture.Proposal, Consent: next}); encoded != nil || !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("nonrepresentable consent identity silently normalized: %q, %v", encoded, err)
			}
			after, err := EncodeStateCheckpoint(original)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed encoding or immutable consent application changed original state: %v", err)
			}
		})
	}
}

func TestCheckpointClosedPendingTransferRoundTrip(t *testing.T) {
	for _, formerIntegrated := range []bool{false, true} {
		name := "former active"
		if formerIntegrated {
			name = "former integrated"
		}
		t.Run(name, func(t *testing.T) {
			schedule, former, target := schedulingPair(t)
			schedule = schedulingAdmit(t, schedulingAdmit(t, schedule, former), target)
			if formerIntegrated {
				schedule = schedulingObserve(t, schedule, former, ObserveIntegrated)
			}
			_, owner := schedulingPolicy(t)
			command := schedulingCommand(t, owner, target.Current().Binding().Reference().ProposalID, target.Current().Carrier(), 1)
			pending := schedulingRequest(t, schedule, command)
			closed := schedulingObserve(t, pending, target, ObserveClosedUnmerged)
			if closed.Generation() != pending.Generation() || closed.CanPromote(former, closed.Generation()) || closed.CanPromote(target, closed.Generation()) {
				t.Fatal("target closure changed the active fence or completed priority transfer")
			}
			encoded, err := EncodeStateCheckpoint(StateCheckpoint{Scheduling: closed})
			if err != nil {
				t.Fatalf("encode legal closed pending target: %v", err)
			}
			restored, err := RestoreStateCheckpoint(encoded)
			if err != nil || !reflect.DeepEqual(restored.Scheduling, closed) {
				t.Fatalf("closed pending transfer lost immutable facts: %+v, %v", restored.Scheduling, err)
			}
			if request, present := restored.Scheduling.PendingTransfer(); !present || request != command {
				t.Fatal("restoration lost the exact pending transfer")
			}
			unresolved := schedulingResolve(t, restored.Scheduling, TransferUnresolved)
			if !reflect.DeepEqual(unresolved, closed) {
				t.Fatal("unresolved reconciliation changed closed pending state")
			}
			withdrawn, err := restored.Scheduling.ResolveTransfer(command.OperationID(), restored.Scheduling.Generation(), FormerUnmergedWithdrawn)
			if !errors.Is(err, ErrScheduleConflict) || !reflect.DeepEqual(withdrawn, closed) {
				t.Fatalf("closed target became active through withdrawal: %v", err)
			}
			merged := schedulingResolve(t, restored.Scheduling, FormerMerged)
			want := schedulingResolve(t, closed, FormerMerged)
			active, present := merged.Active()
			if !reflect.DeepEqual(merged, want) || !present || active.ProposalID() != former.Current().Binding().Reference().ProposalID || active.State() != ScheduleIntegratedPending || merged.Entries()[1].State() != ScheduleClosed || !merged.CanPromote(former, merged.Generation()) || merged.CanPromote(target, merged.Generation()) {
				t.Fatal("restoration prevented reconciliation or activated the closed target")
			}
			if _, present := merged.PendingTransfer(); present {
				t.Fatal("completed former-merge reconciliation remains pending")
			}
			encoded, err = EncodeStateCheckpoint(StateCheckpoint{Scheduling: merged})
			if err != nil {
				t.Fatal(err)
			}
			after, err := RestoreStateCheckpoint(encoded)
			if err != nil || !reflect.DeepEqual(after.Scheduling, merged) {
				t.Fatalf("reconciled transfer did not remain durable: %v", err)
			}
		})
	}
}

func TestCheckpointRejectsInvalidPendingTransferContext(t *testing.T) {
	schedule, former, target := schedulingPair(t)
	schedule = schedulingAdmit(t, schedulingAdmit(t, schedule, former), target)
	_, owner := schedulingPolicy(t)
	command := schedulingCommand(t, owner, target.Current().Binding().Reference().ProposalID, target.Current().Carrier(), 1)
	pending := schedulingRequest(t, schedule, command)
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Scheduling: pending})
	if err != nil {
		t.Fatal(err)
	}
	closed := checkpointJSONChange(t, encoded, "scheduling/Entries/1/State", ScheduleClosed)
	for path, value := range map[string]any{
		"scheduling/Pending/Project": "foreign", "scheduling/Pending/Suite": "foreign", "scheduling/Pending/Proposal": "unknown", "scheduling/Pending/Carrier": "unknown",
		"scheduling/Pending/OperationID": "unrecorded", "scheduling/Pending/Order": 2,
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, closed, path, value)); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("invalid closed pending context accepted: %v", err)
			}
		})
	}
	for _, state := range []ScheduleEntryState{ScheduleActive, ScheduleIntegratedPending, SchedulePromoted} {
		formerState := ScheduleWaiting
		if state == SchedulePromoted {
			formerState = ScheduleActive
		}
		changed := checkpointJSONChange(t, encoded, "scheduling/Entries/0/State", formerState)
		changed = checkpointJSONChange(t, changed, "scheduling/Entries/1/State", state)
		if _, err := RestoreStateCheckpoint(changed); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("illegal pending target state %v accepted: %v", state, err)
		}
	}
	if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, closed, "scheduling/Entries/0/State", ScheduleWaiting)); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("closed pending transfer without former active entry accepted: %v", err)
	}
}
