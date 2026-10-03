package contract

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestCheckpointRejectsInvalidSurrogateEscapes(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Protected: checkpointFixture(t).Protected})
	if err != nil {
		t.Fatal(err)
	}
	for _, escaped := range []string{`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\ud800"`} {
		t.Run(escaped, func(t *testing.T) {
			changed := bytes.Replace(encoded, []byte(`"v1"`), []byte(escaped), 1)
			if bytes.Equal(changed, encoded) {
				t.Fatal("fixture did not change its covered-input identity")
			}
			if _, err := RestoreStateCheckpoint(changed); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("invalid Unicode surrogate identity silently normalized: %v", err)
			}
		})
	}
}

func TestCheckpointRejectsMissingPriorityCommandAndNonrepresentableUnicode(t *testing.T) {
	fixture := checkpointFixture(t)
	t.Run("missing priority receipt command", func(t *testing.T) {
		encoded, err := EncodeStateCheckpoint(StateCheckpoint{Scheduling: fixture.Scheduling})
		if err != nil {
			t.Fatal(err)
		}
		changed := checkpointJSONChange(t, encoded, "scheduling/Results/0/Command", nil)
		if _, err := RestoreStateCheckpoint(changed); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("priority receipt without its command became usable history: %v", err)
		}
	})
	t.Run("truncated Unicode escape", func(t *testing.T) {
		if _, err := RestoreStateCheckpoint([]byte(`{"version":1,"protected":{"Covered":{"runner":"\u00`)); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("truncated Unicode identity became usable state: %v", err)
		}
	})
	t.Run("invalid UTF-8 dictionary identity", func(t *testing.T) {
		key := "runner-" + string([]byte{0xff})
		protected := checkpointValue(NewProtectedContract(fixture.Protected.Manifest(), fixture.Protected.ScopeDigest(), map[string]string{key: "v1"}))
		if encoded, err := EncodeStateCheckpoint(StateCheckpoint{Protected: protected}); encoded != nil || !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("invalid UTF-8 dictionary identity silently normalized: %q, %v", encoded, err)
		}
	})
}

func TestCheckpointRejectsIncompleteJSONAndDuplicateDictionaryKeys(t *testing.T) {
	for _, encoded := range []string{
		`{"version":1,`,
		`{"version":1,"unfinished`,
		`{"version":1,"protected":{"Covered":{"runner":"v1","runner":"v2"}}}`,
		`{"version":1,"protected":{"Covered":{"runner":`,
		`{"version":1,"proposal":{"Revisions":[`,
	} {
		t.Run(encoded, func(t *testing.T) {
			if _, err := RestoreStateCheckpoint([]byte(encoded)); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("malformed persisted document accepted: %v", err)
			}
		})
	}
}

func TestCheckpointRejectsUnserializableRecordedTime(t *testing.T) {
	fixture := checkpointFixture(t)
	record := fixture.History.Record()
	unsupported := checkpointValue(NewPromotionRecord(PromotionRecordInput{
		OperationID: record.OperationID(), VersionID: record.VersionID(), Binding: record.Binding(),
		Carrier: record.Carrier(), Source: record.Source(), Target: record.Target(),
		RecordedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), CorrectsVersionID: record.CorrectsVersionID(),
	}))
	history := checkpointValue(NewHistoricalCanonical(fixture.History.Version(), unsupported))
	if encoded, err := EncodeStateCheckpoint(StateCheckpoint{History: history}); encoded != nil || !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("unsupported persisted time produced usable data: %q, %v", encoded, err)
	}
}

func TestCheckpointPreservesMultipleHistoricalConsentActorsAndRevisions(t *testing.T) {
	fixture := checkpointFixture(t)
	consent := fixture.Consent
	for index, id := range []PrincipalID{"z-self", "a-self", "third-self"} {
		actor := checkpointValue(NewPrincipal(id, Human))
		ref := fixture.Proposal.Current().Binding().Reference()
		if index < 2 {
			ref.RevisionID = "r1"
		}
		command := checkpointValue(NewCommand(CommandInput{
			OperationID: OperationID("revoke-" + id), SourceCommandID: SourceCommandID("source-" + id),
			Actor: actor, Reference: ref, Carrier: fixture.Proposal.Current().Carrier(), Action: RevokeConsent, Order: 2,
		}))
		var result CommandResult
		var err error
		historicalPolicy := checkpointValue(NewPolicy(fixture.Policy.ProjectID(), PolicyRevisionID("historical-owner-"+id), actor))
		consent, result, err = consent.Apply(fixture.Proposal, historicalPolicy, command)
		if err != nil || result.Outcome() != ConsentNoActiveApproval {
			t.Fatalf("historical owner self-revocation fixture failed: %v, %v", result, err)
		}
	}
	want := StateCheckpoint{Proposal: fixture.Proposal, Policy: fixture.Policy, Consent: consent}
	encoded, err := EncodeStateCheckpoint(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreStateCheckpoint(encoded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("historical actors/revisions changed on restore: %v", err)
	}
	if got.Consent.HasApproval(got.Proposal, got.Policy) {
		t.Fatal("historical approval or self-revocation granted current consent")
	}
	changed := checkpointJSONChange(t, encoded, "consent/States/0/Binding/Scope", artifact.Hash([]byte("different historical scope")).String())
	if _, err := RestoreStateCheckpoint(changed); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("actors in one immutable revision accepted different bindings: %v", err)
	}
}

func TestCheckpointRejectsImpossibleConsentRevokeHistory(t *testing.T) {
	fixture := checkpointFixture(t)
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{Consent: fixture.Consent})
	if err != nil {
		t.Fatal(err)
	}
	encoded = checkpointJSONChange(t, encoded, "consent/Results/0/Command/Action", RevokeConsent)
	encoded = checkpointJSONChange(t, encoded, "consent/Results/0/Outcome", ConsentRevoked)
	if _, err := RestoreStateCheckpoint(encoded); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("revocation claimed an absent prior approval: %v", err)
	}
}

func TestCheckpointRejectsBlankOperationAliasAndReasonlessRejection(t *testing.T) {
	encoded, err := EncodeStateCheckpoint(checkpointFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]any{
		"consent/Operations/ ":         "source-approve",
		"result/Outcome":               ConsentRejected,
		"scheduling/Results/0/Outcome": PriorityRejected,
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, encoded, path, value)); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("inconsistent identity or rejection reason accepted: %v", err)
			}
		})
	}
}

func TestCheckpointPreservesHistoricalNoneffectDecisions(t *testing.T) {
	fixture := checkpointFixture(t)
	encoded, err := EncodeStateCheckpoint(StateCheckpoint{PromotionDecision: fixture.PromotionDecision})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []PromotionOutcome{PromotionBlocked, PromotionReady, PromotionNoChange} {
		reason := PromotionReasonNone
		if outcome == PromotionBlocked {
			reason = PromotionReasonApprovalMissing
		}
		changed := checkpointJSONChange(t, encoded, "decision/Effect", nil)
		changed = checkpointJSONChange(t, changed, "decision/Outcome", outcome)
		changed = checkpointJSONChange(t, changed, "decision/Reason", reason)
		got, err := RestoreStateCheckpoint(changed)
		if err != nil || got.PromotionDecision.Outcome() != outcome || got.PromotionDecision.Reason() != reason {
			t.Fatalf("historical noneffect decision changed: %v", err)
		}
		if _, present := got.PromotionDecision.Effect(); present {
			t.Fatal("historical noneffect decision acquired an authority effect")
		}
	}
	for path, value := range map[string]any{"decision/Outcome": PromotionBlocked, "decision/Reason": PromotionReasonApprovalMissing} {
		if _, err := RestoreStateCheckpoint(checkpointJSONChange(t, encoded, path, value)); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("inconsistent proposed decision accepted (%s): %v", path, err)
		}
	}
}

func TestCheckpointIsolatesEncodedInputAndReturnedCollections(t *testing.T) {
	want := checkpointFixture(t)
	encoded, err := EncodeStateCheckpoint(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RestoreStateCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for index := range encoded {
		encoded[index] = 0
	}
	got.Protected.CoveredInputs()["runner"] = "mutated"
	got.Protected.Manifest().Entries()[0].Path = "changed/path"
	got.Consent.Results()[0] = CommandResult{}
	got.Scheduling.Entries()[0] = ScheduleEntry{}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("restored state retained mutable encoded input or exposed mutable collections")
	}
}
