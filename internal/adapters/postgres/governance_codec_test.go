package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestStoredAuthorityRejectsVersionAliases(t *testing.T) {
	owner := codecValue(contract.NewPrincipal("owner", contract.Human))
	policy := codecValue(contract.NewPolicy("project", "policy", owner))
	suite := codecValue(contract.NewSuite("project", "suite", "", 0))
	canonical := codecValue(contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	schedule := codecValue(contract.NewSchedule("project", "suite"))
	encoded, err := encodeAuthority(authorityState{canonical: canonical, policy: policy, scheduling: schedule, target: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAuthority(encoded, "project", "suite", "", "0"); err != nil {
		t.Fatalf("valid stored authority cannot round trip: %v", err)
	}
	for _, alias := range []string{"Version", "VERSION", "verſion"} {
		t.Run(alias, func(t *testing.T) {
			mutated := bytes.Replace(encoded, []byte(`"version":1`), []byte(`"version":99,"`+alias+`":1`), 1)
			if bytes.Equal(encoded, mutated) {
				t.Fatal("fixture did not replace the version field")
			}
			if _, err := decodeAuthority(mutated, "project", "suite", "", "0"); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("unsupported authority version hidden by field alias %q was accepted: %v", alias, err)
			}
		})
	}
}

type codecFixture struct {
	state     authorityState
	input     contract.PromotionInput
	promotion governance.OperationReceipt
	consent   governance.OperationReceipt
	command   contract.Command
}

func newCodecFixture(t *testing.T) codecFixture {
	t.Helper()
	owner := codecValue(contract.NewPrincipal("owner", contract.Human))
	policy := codecValue(contract.NewPolicy("project", "policy", owner))
	manifest := codecValue(artifact.NewManifest([]artifact.Entry{{Path: "tests/test.txt", Content: artifact.Hash([]byte("protected"))}}))
	protected := codecValue(contract.NewProtectedContract(manifest, artifact.Hash([]byte("scope")), map[string]string{"runner": "v1", "Runner": "v2"}))
	reference := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}
	binding := codecValue(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs()}))
	proposal := codecValue(contract.NewProposal(codecValue(contract.NewProposalRevision(binding, "origin", "carrier"))))
	consent := codecValue(contract.NewConsent("project", "suite", "proposal"))
	command := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "approve", SourceCommandID: "source-approve", Actor: owner, Reference: reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	consent, result, err := consent.Apply(proposal, policy, command)
	if err != nil {
		t.Fatal(err)
	}
	schedule := codecValue(contract.NewSchedule("project", "suite"))
	schedule = codecValue(schedule.Admit(proposal, true))
	canonical := codecValue(contract.NewCanonicalSnapshot(codecValue(contract.NewSuite("project", "suite", "", 0)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	evidence := codecValue(contract.NewIntegrityEvidence("checker", "integrated", binding, contract.IntegrityPassed))
	assessment := codecValue(contract.AssessIntegrity("integrated", binding, &evidence))
	integration := codecValue(contract.NewIntegration("project", "main", "integrated", "carrier", contract.IntegrationMergedChange))
	input := contract.PromotionInput{Context: contract.PromotionContext{Canonical: canonical, Proposed: protected, Proposal: proposal, Reference: reference, Carrier: "carrier", Policy: policy, Consent: consent, Assessment: assessment, Scheduling: schedule, ExpectedStateRevision: 0, ExpectedSchedulingGeneration: schedule.Generation()}, Integration: integration, Target: "main", OperationID: "promote", NewVersionID: "v1", RecordedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	decision := codecValue(contract.DecidePromotion(input))
	if _, present := decision.Effect(); !present {
		t.Fatal("codec fixture promotion has no effect")
	}
	request := governance.PromoteRequest{OperationID: input.OperationID, Reference: reference, Carrier: "carrier", Proposed: protected, AssessmentSource: "integrated", Integration: integration, NewVersionID: "v1", RecordedAt: input.RecordedAt}
	return codecFixture{
		state:     authorityState{canonical: canonical, policy: policy, scheduling: schedule, target: "main", proposals: map[contract.ProposalID]contract.Proposal{"proposal": proposal}, consents: map[contract.ProposalID]contract.Consent{"proposal": consent}, assessments: map[assessmentKey]contract.IntegrityAssessment{{reference, "integrated"}: assessment}},
		input:     input,
		promotion: governance.OperationReceipt{Kind: governance.OperationPromote, Promotion: governance.PromotionReceipt{Identity: governance.PromotionIdentity{Kind: governance.OperationPromote, Request: request, Binding: binding}, Decision: decision}},
		consent:   governance.OperationReceipt{Kind: governance.OperationConsent, Consent: governance.ConsentReceipt{Result: result, EvaluatedReference: reference, PolicyRevisionID: "policy", CurrentApprovalEligible: true}},
		command:   command,
	}
}

func codecJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func codecCheckpoint(t *testing.T, value contract.StateCheckpoint) json.RawMessage {
	t.Helper()
	encoded, err := contract.EncodeStateCheckpoint(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestStoredAuthorityRoundTripPreservesFacts(t *testing.T) {
	fixture := newCodecFixture(t)
	encoded := codecValue(encodeAuthority(fixture.state))
	restored := codecValue(decodeAuthority(encoded, "project", "suite", "", "0"))
	if !reflect.DeepEqual(restored, fixture.state) {
		t.Fatal("authority round trip changed immutable facts or case-sensitive covered input identities")
	}
	// Returned facts retain their immutability after persistence restoration.
	covered := restored.proposals["proposal"].Current().Binding().CoveredInputs()
	covered["runner"] = "tampered"
	if restored.proposals["proposal"].Current().Binding().CoveredInputs()["runner"] != "v1" {
		t.Fatal("restored binding exposed its covered input map")
	}
}

func TestStoredAuthorityRejectsInconsistentFacts(t *testing.T) {
	fixture := newCodecFixture(t)
	encoded := codecValue(encodeAuthority(fixture.state))
	empty := codecCheckpoint(t, contract.StateCheckpoint{})
	base := func(policy contract.Policy, schedule contract.Schedule) json.RawMessage {
		return codecCheckpoint(t, contract.StateCheckpoint{Canonical: fixture.state.canonical, Policy: policy, Scheduling: schedule})
	}
	foreignPolicy := codecValue(contract.NewPolicy("foreign", "policy", codecValue(contract.NewPrincipal("owner", contract.Human))))
	foreignSchedule := codecValue(contract.NewSchedule("project", "foreign"))
	ref := fixture.input.Context.Reference
	otherRef := ref
	otherRef.ProposalID = "unknown"
	otherBinding := codecValue(contract.NewApprovalBinding(contract.BindingInput{Reference: otherRef, Manifest: fixture.input.Context.Proposed.Manifest().Digest(), Scope: fixture.input.Context.Proposed.ScopeDigest(), PolicyRevision: "policy"}))
	otherProposal := codecValue(contract.NewProposal(codecValue(contract.NewProposalRevision(otherBinding, "origin", "other-carrier"))))
	unknownSchedule := codecValue(codecValue(contract.NewSchedule("project", "suite")).Admit(otherProposal, true))
	wrongCarrierProposal := codecValue(contract.NewProposal(codecValue(contract.NewProposalRevision(fixture.input.Context.Proposal.Current().Binding(), "origin", "changed-carrier"))))
	wrongCarrierSchedule := codecValue(codecValue(contract.NewSchedule("project", "suite")).Admit(wrongCarrierProposal, true))
	unknownAssessment := codecValue(contract.AssessIntegrity("integrated", otherBinding, nil))
	mismatchBinding := codecValue(contract.NewApprovalBinding(contract.BindingInput{Reference: ref, Manifest: artifact.Hash([]byte("different manifest")), Scope: fixture.input.Context.Proposed.ScopeDigest(), PolicyRevision: "policy"}))
	mismatchAssessment := codecValue(contract.AssessIntegrity("integrated", mismatchBinding, nil))
	foreignRef := ref
	foreignRef.ProjectID = "foreign"
	foreignBinding := codecValue(contract.NewApprovalBinding(contract.BindingInput{Reference: foreignRef, Manifest: fixture.input.Context.Proposed.Manifest().Digest(), Scope: fixture.input.Context.Proposed.ScopeDigest(), PolicyRevision: "policy"}))
	foreignProposal := codecValue(contract.NewProposal(codecValue(contract.NewProposalRevision(foreignBinding, "origin", "carrier"))))
	foreignConsent := codecValue(contract.NewConsent("foreign", "suite", "proposal"))
	foreignAssessment := codecValue(contract.AssessIntegrity("integrated", foreignBinding, nil))
	cases := []struct {
		name   string
		mutate func(*authorityPayload)
	}{
		{"unsupported version", func(p *authorityPayload) { p.Version = 2 }},
		{"blank target", func(p *authorityPayload) { p.Target = " \t" }},
		{"missing canonical", func(p *authorityPayload) { p.Base = empty }},
		{"invalid base checkpoint", func(p *authorityPayload) { p.Base = json.RawMessage(`{"version":99}`) }},
		{"missing policy", func(p *authorityPayload) { p.Base = base(contract.Policy{}, fixture.state.scheduling) }},
		{"foreign policy", func(p *authorityPayload) { p.Base = base(foreignPolicy, fixture.state.scheduling) }},
		{"missing schedule", func(p *authorityPayload) { p.Base = base(fixture.state.policy, contract.Schedule{}) }},
		{"foreign schedule", func(p *authorityPayload) { p.Base = base(fixture.state.policy, foreignSchedule) }},
		{"unknown scheduled proposal", func(p *authorityPayload) { p.Base = base(fixture.state.policy, unknownSchedule) }},
		{"wrong scheduled carrier", func(p *authorityPayload) { p.Base = base(fixture.state.policy, wrongCarrierSchedule) }},
		{"invalid proposal checkpoint", func(p *authorityPayload) { p.Proposals = []json.RawMessage{json.RawMessage(`{"version":99}`)} }},
		{"missing proposal", func(p *authorityPayload) { p.Proposals = []json.RawMessage{empty} }},
		{"missing consent", func(p *authorityPayload) {
			p.Proposals = []json.RawMessage{codecCheckpoint(t, contract.StateCheckpoint{Proposal: fixture.input.Context.Proposal})}
		}},
		{"duplicate proposal", func(p *authorityPayload) { p.Proposals = append(p.Proposals, p.Proposals[0]) }},
		{"foreign proposal", func(p *authorityPayload) {
			p.Proposals = []json.RawMessage{codecCheckpoint(t, contract.StateCheckpoint{Proposal: foreignProposal, Consent: foreignConsent})}
		}},
		{"invalid assessment checkpoint", func(p *authorityPayload) { p.Assessments = []json.RawMessage{json.RawMessage(`{"version":99}`)} }},
		{"missing assessment", func(p *authorityPayload) { p.Assessments = []json.RawMessage{empty} }},
		{"duplicate assessment", func(p *authorityPayload) { p.Assessments = append(p.Assessments, p.Assessments[0]) }},
		{"foreign assessment", func(p *authorityPayload) {
			p.Assessments = []json.RawMessage{codecCheckpoint(t, contract.StateCheckpoint{Assessment: foreignAssessment})}
		}},
		{"unknown assessed proposal", func(p *authorityPayload) {
			p.Assessments = []json.RawMessage{codecCheckpoint(t, contract.StateCheckpoint{Assessment: unknownAssessment})}
		}},
		{"mismatched assessed binding", func(p *authorityPayload) {
			p.Assessments = []json.RawMessage{codecCheckpoint(t, contract.StateCheckpoint{Assessment: mismatchAssessment})}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var payload authorityPayload
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&payload)
			_, err := decodeAuthority(codecJSON(t, payload), "project", "suite", "", "0")
			if !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("inconsistent persisted authority accepted: %v", err)
			}
		})
	}
	for _, tt := range []struct{ project, suite, current, revision string }{
		{"foreign", "suite", "", "0"}, {"project", "foreign", "", "0"}, {"project", "suite", "v1", "0"}, {"project", "suite", "", "1"}, {"project", "suite", "", "-1"}, {"project", "suite", "", "18446744073709551616"},
	} {
		if _, err := decodeAuthority(encoded, tt.project, tt.suite, tt.current, tt.revision); !errors.Is(err, governance.ErrInvalidSnapshot) {
			t.Fatalf("authority did not bind stored row identity/fence %+v: %v", tt, err)
		}
	}
}

func TestStoredReceiptsRoundTripPreservesOutcomes(t *testing.T) {
	fixture := newCodecFixture(t)
	for _, receipt := range []governance.OperationReceipt{fixture.consent, fixture.promotion} {
		operation := "promote"
		if receipt.Kind == governance.OperationConsent {
			operation = "approve"
		}
		encoded := codecValue(encodeReceipt(receipt, contract.OperationID(operation)))
		restored := codecValue(decodeReceipt(encoded, int16(receipt.Kind), "project", "suite", operation))
		if !reflect.DeepEqual(restored, receipt) {
			t.Fatalf("receipt round trip changed original outcome: %+v", restored)
		}
	}
	// A source alias remains keyed by its own operation, retaining the original result.
	encoded := codecValue(encodeReceipt(fixture.consent, "alias"))
	restored := codecValue(decodeReceipt(encoded, int16(governance.OperationConsent), "project", "suite", "alias"))
	if !reflect.DeepEqual(restored, fixture.consent) {
		t.Fatal("alias receipt changed the source's original consent result")
	}
}

func TestStoredReceiptsRejectInconsistentFacts(t *testing.T) {
	fixture := newCodecFixture(t)
	consentEncoded := codecValue(encodeReceipt(fixture.consent, "approve"))
	promotionEncoded := codecValue(encodeReceipt(fixture.promotion, "promote"))
	empty := codecCheckpoint(t, contract.StateCheckpoint{})
	_, duplicate, err := fixture.state.consents["proposal"].Apply(fixture.input.Context.Proposal, fixture.state.policy, fixture.command)
	if err != nil || !duplicate.Duplicate() {
		t.Fatalf("duplicate fixture: %v", err)
	}
	blockedInput := fixture.input
	blockedInput.Integration = contract.Integration{}
	blocked := codecValue(contract.DecidePromotion(blockedInput))
	cases := []struct {
		name   string
		base   []byte
		kind   governance.OperationKind
		op     string
		mutate func(*receiptPayload)
	}{
		{"unsupported version", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Version = 2 }},
		{"wrong stored kind", consentEncoded, governance.OperationPromote, "approve", func(*receiptPayload) {}},
		{"wrong stored operation", consentEncoded, governance.OperationConsent, "other", func(*receiptPayload) {}},
		{"blank operation", consentEncoded, governance.OperationConsent, " ", func(p *receiptPayload) { p.Operation = " " }},
		{"missing consent", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent = nil }},
		{"both outcome branches", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Promotion = &promotionPayload{} }},
		{"invalid consent checkpoint", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.Domain = json.RawMessage(`{"version":99}`) }},
		{"missing command result", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.Domain = empty }},
		{"duplicate command result", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) {
			p.Consent.Domain = codecCheckpoint(t, contract.StateCheckpoint{CommandResult: duplicate})
		}},
		{"foreign evaluated proposal", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.EvaluatedReference.ProposalID = "foreign" }},
		{"foreign evaluated project", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.EvaluatedReference.ProjectID = "foreign" }},
		{"foreign evaluated suite", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.EvaluatedReference.SuiteID = "foreign" }},
		{"missing evaluated revision", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.EvaluatedReference.RevisionID = "" }},
		{"blank policy revision", consentEncoded, governance.OperationConsent, "approve", func(p *receiptPayload) { p.Consent.PolicyRevision = " \t" }},
		{"unknown kind", promotionEncoded, 9, "promote", func(p *receiptPayload) { p.Kind = 9 }},
		{"zero kind", promotionEncoded, 0, "promote", func(p *receiptPayload) { p.Kind = 0 }},
		{"missing promotion", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Promotion = nil }},
		{"promotion with consent", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Consent = &consentPayload{} }},
		{"invalid promotion checkpoint", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Promotion.Domain = json.RawMessage(`{"version":99}`) }},
		{"missing promotion effect", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Promotion.Domain = empty }},
		{"blocked promotion effect", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) {
			p.Promotion.Domain = codecCheckpoint(t, contract.StateCheckpoint{PromotionDecision: blocked})
		}},
		{"foreign promotion project", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Promotion.Reference.ProjectID = "foreign" }},
		{"foreign promotion suite", promotionEncoded, governance.OperationPromote, "promote", func(p *receiptPayload) { p.Promotion.Reference.SuiteID = "foreign" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var payload receiptPayload
			if err := json.Unmarshal(tt.base, &payload); err != nil {
				t.Fatal(err)
			}
			tt.mutate(&payload)
			if _, err := decodeReceipt(codecJSON(t, payload), int16(tt.kind), "project", "suite", tt.op); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("inconsistent persisted receipt accepted: %v", err)
			}
		})
	}
	for _, tt := range []struct{ project, suite string }{{"foreign", "suite"}, {"project", "foreign"}} {
		if _, err := decodeReceipt(consentEncoded, int16(governance.OperationConsent), tt.project, tt.suite, "approve"); !errors.Is(err, governance.ErrInvalidSnapshot) {
			t.Fatalf("consent receipt scope mismatch accepted: %v", err)
		}
	}
}

func TestStoredPayloadsRejectMalformedEnvelopes(t *testing.T) {
	fixture := newCodecFixture(t)
	authority := codecValue(encodeAuthority(fixture.state))
	promotion := codecValue(encodeReceipt(fixture.promotion, "promote"))
	consent := codecValue(encodeReceipt(fixture.consent, "approve"))
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"empty", nil}, {"object truncation", []byte(`{`)}, {"value truncation", []byte(`{"version":`)}, {"field truncation", []byte(`{"version":1,`)}, {"unclosed object", []byte(`{"version":1`)},
		{"wrong object shape", []byte(`[]`)}, {"scalar", []byte(`1`)}, {"unknown field", []byte(`{"unsupported":1}`)},
		{"duplicate field", bytes.Replace(authority, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)},
		{"wrong version type", bytes.Replace(authority, []byte(`"version":1`), []byte(`"version":"1"`), 1)},
		{"wrong proposals shape", bytes.Replace(authority, []byte(`"proposals":[`), []byte(`"proposals":{`), 1)},
		{"object instead of proposal array", []byte(`{"version":1,"base":null,"target":"main","proposals":{}}`)},
		{"null authority", []byte(`null`)},
		{"trailing object", append(append([]byte{}, authority...), []byte(` {}`)...)},
		{"trailing malformed input", append(append([]byte{}, authority...), byte('{'))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeAuthority(tt.data, "project", "suite", "", "0"); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("malformed authority envelope accepted: %v", err)
			}
		})
	}
	for _, tt := range []struct {
		name, from, to string
		data           []byte
		kind           governance.OperationKind
		op             string
	}{
		{"receipt version alias", `"Version":1`, `"Version":99,"version":1`, consent, governance.OperationConsent, "approve"},
		{"receipt duplicate field", `"Version":1`, `"Version":1,"Version":1`, consent, governance.OperationConsent, "approve"},
		{"nested consent alias", `"PolicyRevision":"policy"`, `"PolicyRevision":"policy","policyrevision":"hidden"`, consent, governance.OperationConsent, "approve"},
		{"nested reference alias", `"ProjectID":"project"`, `"ProjectID":"project","projectid":"hidden"`, promotion, governance.OperationPromote, "promote"},
		{"invalid time primitive", `"RecordedAt":"2026-10-02T12:00:00Z"`, `"RecordedAt":"invalid"`, promotion, governance.OperationPromote, "promote"},
		{"wrong pointer shape", `"Consent":{`, `"Consent":[`, consent, governance.OperationConsent, "approve"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mutated := bytes.Replace(tt.data, []byte(tt.from), []byte(tt.to), 1)
			if bytes.Equal(mutated, tt.data) {
				t.Fatal("malformed receipt fixture did not replace field")
			}
			if _, err := decodeReceipt(mutated, int16(tt.kind), "project", "suite", tt.op); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("malformed receipt envelope accepted: %v", err)
			}
		})
	}
}

func TestStoredAuthorityEncodingRejectsInconsistentConsent(t *testing.T) {
	fixture := newCodecFixture(t)
	fixture.state.consents["proposal"] = codecValue(contract.NewConsent("foreign", "suite", "proposal"))
	if _, err := encodeAuthority(fixture.state); !errors.Is(err, contract.ErrInvalidCheckpoint) {
		t.Fatalf("encoder accepted mismatched proposal consent scope: %v", err)
	}
}

func TestStoredPromotionEncodingRejectsUnrepresentableTime(t *testing.T) {
	fixture := newCodecFixture(t)
	fixture.input.RecordedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	decision := codecValue(contract.DecidePromotion(fixture.input))
	fixture.promotion.Promotion.Decision = decision
	fixture.promotion.Promotion.Identity.Request.RecordedAt = fixture.input.RecordedAt
	if _, err := encodeReceipt(fixture.promotion, "promote"); !errors.Is(err, contract.ErrInvalidCheckpoint) {
		t.Fatalf("encoder accepted domain effect with unrepresentable stored time: %v", err)
	}
	effect, present := decision.Effect()
	if !present {
		t.Fatal("unrepresentable time fixture has no effect")
	}
	fixture.state.canonical = codecValue(contract.NewCanonicalSnapshot(effect.Suite(), effect.Version(), fixture.input.Context.Proposed, effect.Promotion()))
	if _, err := encodeAuthority(fixture.state); !errors.Is(err, contract.ErrInvalidCheckpoint) {
		t.Fatalf("encoder accepted canonical history with unrepresentable stored time: %v", err)
	}
}

func TestStoredEncodingPropagatesExactStringRejection(t *testing.T) {
	fixture := newCodecFixture(t)
	lossy := string([]byte{0xff})
	assessment := codecValue(contract.AssessIntegrity(contract.SourceRevision(lossy), fixture.input.Context.Proposal.Current().Binding(), nil))
	fixture.state.assessments = map[assessmentKey]contract.IntegrityAssessment{{fixture.input.Context.Reference, contract.SourceRevision(lossy)}: assessment}
	if _, err := encodeAuthority(fixture.state); !errors.Is(err, contract.ErrInvalidCheckpoint) {
		t.Fatalf("authority encoder suppressed an unrepresentable assessment source: %v", err)
	}
	command := codecValue(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(lossy), SourceCommandID: "another-source", Actor: fixture.command.Actor(), Reference: fixture.command.Reference(), Carrier: fixture.command.Carrier(), Action: contract.RevokeConsent, Order: 2}))
	_, result, err := fixture.state.consents["proposal"].Apply(fixture.input.Context.Proposal, fixture.state.policy, command)
	if err != nil {
		t.Fatal(err)
	}
	fixture.consent.Consent.Result = result
	if _, err := encodeReceipt(fixture.consent, command.OperationID()); !errors.Is(err, contract.ErrInvalidCheckpoint) {
		t.Fatalf("receipt encoder suppressed an unrepresentable original command identity: %v", err)
	}
}

func TestStoredAuthorityRejectsLossyTargetEncoding(t *testing.T) {
	fixture := newCodecFixture(t)
	fixture.state.target = contract.IntegrationTargetID(string([]byte{'t', 0xff}))
	encoded, err := encodeAuthority(fixture.state)
	if err != nil {
		return
	}
	restored, err := decodeAuthority(encoded, "project", "suite", "", "0")
	if err != nil {
		t.Fatalf("authority encoder emitted an unreadable target: %v", err)
	}
	if restored.target != fixture.state.target {
		t.Fatalf("stored integration target lost exact identity: original %q, restored %q; encoding must reject unrepresentable target bytes", fixture.state.target, restored.target)
	}
}

func TestStoredReceiptRejectsLossyEnvelopeEncoding(t *testing.T) {
	fixture := newCodecFixture(t)
	lossy := string([]byte{'a', 0xff})
	t.Run("reserved alias operation", func(t *testing.T) {
		encoded, err := encodeReceipt(fixture.consent, contract.OperationID(lossy))
		if err != nil {
			return
		}
		var payload receiptPayload
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Operation != contract.OperationID(lossy) {
			t.Fatalf("reserved receipt alias lost exact operation identity: original %q, encoded %q", lossy, payload.Operation)
		}
	})
	t.Run("evaluated policy identity", func(t *testing.T) {
		receipt := fixture.consent
		receipt.Consent.PolicyRevisionID = contract.PolicyRevisionID(lossy)
		encoded, err := encodeReceipt(receipt, "approve")
		if err != nil {
			return
		}
		restored, err := decodeReceipt(encoded, int16(governance.OperationConsent), "project", "suite", "approve")
		if err != nil {
			t.Fatalf("receipt encoder emitted unreadable policy identity: %v", err)
		}
		if restored.Consent.PolicyRevisionID != receipt.Consent.PolicyRevisionID {
			t.Fatalf("receipt evaluated policy lost exact identity: original %q, restored %q", receipt.Consent.PolicyRevisionID, restored.Consent.PolicyRevisionID)
		}
	})
}

func TestStoredPayloadsRejectInvalidUTF8(t *testing.T) {
	fixture := newCodecFixture(t)
	authority := codecValue(encodeAuthority(fixture.state))
	authority = bytes.Replace(authority, []byte(`"target":"main"`), []byte{'"', 't', 'a', 'r', 'g', 'e', 't', '"', ':', '"', 'm', 0xff, '"'}, 1)
	if _, err := decodeAuthority(authority, "project", "suite", "", "0"); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Errorf("raw invalid UTF-8 authority target was silently decoded: %v", err)
	}
	receipt := codecValue(encodeReceipt(fixture.consent, "approve"))
	receipt = bytes.Replace(receipt, []byte(`"PolicyRevision":"policy"`), append(append([]byte(`"PolicyRevision":"p`), byte(0xff)), byte('"')), 1)
	if _, err := decodeReceipt(receipt, int16(governance.OperationConsent), "project", "suite", "approve"); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Errorf("raw invalid UTF-8 receipt policy identity was silently decoded: %v", err)
	}
}

func TestStoredPayloadsRejectUnpairedSurrogateEscapes(t *testing.T) {
	fixture := newCodecFixture(t)
	for _, escape := range []string{`\ud800`, `\udc00`, `\ud800x`, `\ud800\ud800`} {
		t.Run(escape, func(t *testing.T) {
			authority := codecValue(encodeAuthority(fixture.state))
			authority = bytes.Replace(authority, []byte(`"target":"main"`), []byte(`"target":"main`+escape+`"`), 1)
			if _, err := decodeAuthority(authority, "project", "suite", "", "0"); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Errorf("unpaired surrogate authority target was silently replaced: %v", err)
			}
			receipt := codecValue(encodeReceipt(fixture.consent, "approve"))
			receipt = bytes.Replace(receipt, []byte(`"PolicyRevision":"policy"`), []byte(`"PolicyRevision":"policy`+escape+`"`), 1)
			if _, err := decodeReceipt(receipt, int16(governance.OperationConsent), "project", "suite", "approve"); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Errorf("unpaired surrogate evaluated policy identity was silently replaced: %v", err)
			}
		})
	}
	// Legal surrogate pairs and literal replacement characters remain exact values.
	for _, tt := range []struct{ literal, escaped string }{{"main𝄞", `main\ud834\udd1e`}, {"main�", `main\ufffd`}, {`main\ud800`, `main\\ud800`}} {
		fixture.state.target = contract.IntegrationTargetID(tt.literal)
		authority := codecValue(encodeAuthority(fixture.state))
		authority = bytes.Replace(authority, []byte(tt.literal), []byte(tt.escaped), 1)
		restored, err := decodeAuthority(authority, "project", "suite", "", "0")
		if err != nil || restored.target != fixture.state.target {
			t.Fatalf("legal escaped target changed identity: %q, %v", restored.target, err)
		}
	}
}

func codecValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
