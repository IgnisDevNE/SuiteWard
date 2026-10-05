package postgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

var (
	codecTime   = time.Date(2026, 10, 2, 12, 0, 0, 123456000, time.UTC)
	codecScope  = artifact.Hash([]byte("tests/**"))
	codecTarget = contract.IntegrationTargetID("default")
)

// codecWorld is one valid proposal, approval, promotion and receipts, from
// which each test derives a stored payload and then damages it.
type codecWorld struct {
	manifest    artifact.Manifest
	protected   contract.ProtectedContract
	binding     contract.ApprovalBinding
	owner       contract.Principal
	command     contract.Command
	result      contract.CommandResult
	duplicate   contract.CommandResult
	integration contract.Integration
	record      contract.PromotionRecord
	consent     governance.ConsentReceipt
	promotion   governance.PromotionReceipt
}

func newCodecWorld() codecWorld {
	w := codecWorld{}
	w.manifest = must(artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash([]byte("contract"))}}))
	w.protected = must(contract.NewProtectedContract(w.manifest, codecScope, map[string]string{"runner": "v1"}))
	reference := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1"}
	w.binding = must(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: "v0", Manifest: w.manifest.Digest(), Scope: codecScope,
		PolicyRevision: "policy-1", CoveredInputs: map[string]string{"runner": "v1"}}))
	w.owner = must(contract.NewPrincipal("owner", contract.Human))
	w.command = must(contract.NewCommand(contract.CommandInput{OperationID: "op-1", SourceCommandID: "src-1", Actor: w.owner, Reference: reference, Carrier: "carrier-1", Action: contract.ApproveConsent, Order: 1}))
	w.result = must(contract.ReconstituteCommandResult(w.command, contract.ConsentApproved, contract.ConsentReasonNone))

	proposal := must(contract.NewProposal(must(contract.NewProposalRevision(w.binding, "origin", "carrier-1"))))
	policy := must(contract.NewPolicy("project", "policy-1", w.owner))
	consent, _, err := must(contract.NewConsent("project", "suite", "p1")).Apply(proposal, policy, w.command)
	if err != nil {
		panic(err)
	}
	replay := must(contract.NewCommand(contract.CommandInput{OperationID: "op-2", SourceCommandID: "src-1", Actor: w.owner, Reference: reference, Carrier: "carrier-1", Action: contract.ApproveConsent, Order: 1}))
	if _, w.duplicate, err = consent.Apply(proposal, policy, replay); err != nil || !w.duplicate.Duplicate() {
		panic("fixture duplicate")
	}

	w.integration = must(contract.NewIntegration("project", codecTarget, "merged-1", "carrier-1", contract.IntegrationMergedChange))
	w.record = must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "promote-1", VersionID: "v1", Binding: w.binding, Carrier: "carrier-1", Source: "merged-1", Target: codecTarget, RecordedAt: codecTime}))
	w.consent = governance.ConsentReceipt{Result: w.result, EvaluatedReference: reference, PolicyRevisionID: "policy-1", CurrentApprovalEligible: true}
	request := governance.PromoteRequest{OperationID: "promote-1", Reference: reference, Carrier: "carrier-1", Proposed: w.protected, AssessmentSource: "merged-1", Integration: w.integration, NewVersionID: "v1", RecordedAt: codecTime}
	w.promotion = governance.PromotionReceipt{Identity: governance.PromotionIdentity{Kind: governance.OperationPromote, Request: request, Binding: w.binding}, Record: w.record}
	return w
}

// operationRow is the stored operations row of an encoded receipt.
func operationRow(t *testing.T, kind governance.OperationKind, consent *governance.ConsentReceipt, promotion *governance.PromotionReceipt) dbgen.Operation {
	t.Helper()
	code, raw, err := encodeReceipt(kind, consent, promotion)
	if err != nil {
		t.Fatal(err)
	}
	return dbgen.Operation{OperationID: "op", ProjectID: "project", SuiteID: "suite", Kind: code, Receipt: raw}
}

func TestReceiptsRoundTripThroughTheirStoredPayload(t *testing.T) {
	w := newCodecWorld()
	t.Run("consent", func(t *testing.T) {
		row := operationRow(t, governance.OperationConsent, &w.consent, nil)
		got, err := decodeReceipt(row)
		if err != nil || got.Kind != governance.OperationConsent || got.Consent == nil || got.Promotion != nil || got.Consent.Result.Command() != w.command ||
			got.Consent.Result.Outcome() != contract.ConsentApproved || !got.Consent.CurrentApprovalEligible || got.Consent.PolicyRevisionID != "policy-1" {
			t.Fatalf("decoded %+v, %v", got, err)
		}
	})
	for _, kind := range []governance.OperationKind{governance.OperationPromote, governance.OperationBootstrap, governance.OperationCorrect} {
		t.Run(string(kind), func(t *testing.T) {
			promotion := w.promotion
			promotion.Identity.Kind = kind
			got, err := decodeReceipt(operationRow(t, kind, nil, &promotion))
			if err != nil || got.Kind != kind || got.Promotion == nil || got.Consent != nil || !got.Promotion.Record.Binding().Equal(w.binding) ||
				got.Promotion.Identity.Request.Integration != w.integration || !got.Promotion.Identity.Request.RecordedAt.Equal(codecTime) {
				t.Fatalf("decoded %+v, %v", got, err)
			}
		})
	}
}

func TestEncodeReceiptRefusesWhatCannotBeStored(t *testing.T) {
	w := newCodecWorld()
	zeroIntegration := w.promotion
	zeroIntegration.Identity.Request.Integration = contract.Integration{}
	cases := []struct {
		name      string
		kind      governance.OperationKind
		consent   *governance.ConsentReceipt
		promotion *governance.PromotionReceipt
	}{
		{"an unknown operation kind", "audit", nil, nil},
		{"a consent without its body", governance.OperationConsent, nil, nil},
		{"a consent carrying a promotion", governance.OperationConsent, &w.consent, &w.promotion},
		{"a promotion without its body", governance.OperationPromote, nil, nil},
		{"a promotion carrying a consent", governance.OperationPromote, &w.consent, &w.promotion},
		{"a promotion carrying only a consent", governance.OperationPromote, &w.consent, nil},
		{"a duplicate consent result", governance.OperationConsent, &governance.ConsentReceipt{Result: w.duplicate}, nil},
		{"a promotion with an unknown integration kind", governance.OperationPromote, nil, &zeroIntegration},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if code, raw, err := encodeReceipt(tt.kind, tt.consent, tt.promotion); err == nil || code != "" || raw != nil {
				t.Fatalf("encodeReceipt = %q, %s, %v; want an error and nothing to store", code, raw, err)
			}
		})
	}
}

func TestEncodeRefusesValuesWithoutACode(t *testing.T) {
	w := newCodecWorld()
	if _, err := encodePrincipal(contract.Principal{}); !errors.Is(err, errUnknownCode) {
		t.Fatalf("encodePrincipal of an unconstructed principal = %v; want errUnknownCode", err)
	}
	if _, err := encodeCommand(contract.Command{}); !errors.Is(err, errUnknownCode) {
		t.Fatalf("encodeCommand of an unconstructed command = %v; want errUnknownCode", err)
	}
	if _, err := encodeIntegration(contract.Integration{}); !errors.Is(err, errUnknownCode) {
		t.Fatalf("encodeIntegration of an unconstructed integration = %v; want errUnknownCode", err)
	}
	if _, err := encodeConsentReceipt(governance.ConsentReceipt{Result: w.duplicate}); err == nil {
		t.Fatal("a receipt recorded the duplicate instead of the original result")
	}
	if _, err := encodeConsentReceipt(governance.ConsentReceipt{}); !errors.Is(err, errUnknownCode) {
		t.Fatalf("encodeConsentReceipt of an empty receipt = %v; want errUnknownCode", err)
	}
	if got := nonNil(nil); got == nil || len(got) != 0 {
		t.Fatalf("nonNil(nil) = %#v; want an empty map so JSON stores {}", got)
	}
	covered, err := encodeCoveredInputs(contract.ApprovalBinding{})
	if err != nil || string(covered) != "{}" {
		t.Fatalf("covered inputs of a binding without any = %s, %v; want {}", covered, err)
	}
}

func TestDecodeStrictRejectsMalformedJSON(t *testing.T) {
	cases := map[string]string{
		"unknown field":      `{"entries":[],"extra":1}`,
		"wrong type":         `{"entries":"none"}`,
		"not an object":      `[1]`,
		"truncated":          `{"entries":[`,
		"empty":              ``,
		"nested wrong type":  `{"entries":[{"path":7,"content":"x"}]}`,
		"nested unknown key": `{"entries":[{"path":"a","content":"x","mode":"0644"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var dto manifestDTO
			if err := decodeStrict([]byte(raw), &dto); err == nil {
				t.Fatalf("decodeStrict accepted %q", raw)
			}
		})
	}
	var dto manifestDTO
	if err := decodeStrict([]byte(`{"entries":[]}`), &dto); err != nil {
		t.Fatalf("decodeStrict of a valid payload: %v", err)
	}
}

func TestDecodeManifestChecksTheStoredDigest(t *testing.T) {
	w := newCodecWorld()
	raw := must(encodeManifest(w.manifest))
	got, err := decodeManifest(raw, w.manifest.Digest().String())
	if err != nil || got.Digest() != w.manifest.Digest() {
		t.Fatalf("decodeManifest of its own encoding = %v, %v", got.Digest(), err)
	}
	other := artifact.Hash([]byte("another manifest")).String()
	validDigest := artifact.Hash([]byte("x")).String()
	cases := []struct{ name, raw, digest string }{
		{"a digest that does not match", string(raw), other},
		{"an unknown field", `{"entries":[],"owner":"x"}`, w.manifest.Digest().String()},
		{"an unparsable content digest", `{"entries":[{"path":"tests/a.txt","content":"md5:abc"}]}`, w.manifest.Digest().String()},
		{"an invalid path", `{"entries":[{"path":"../escape","content":"` + validDigest + `"}]}`, w.manifest.Digest().String()},
		{"a duplicate path", `{"entries":[{"path":"a","content":"` + validDigest + `"},{"path":"a","content":"` + validDigest + `"}]}`, w.manifest.Digest().String()},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeManifest([]byte(tt.raw), tt.digest); err == nil {
				t.Fatal("decodeManifest accepted a damaged manifest")
			}
		})
	}
}

func TestDecodeRevisionBindingRejectsDamagedRows(t *testing.T) {
	w := newCodecWorld()
	row := func(change func(*dbgen.ProposalRevision)) dbgen.ProposalRevision {
		r := dbgen.ProposalRevision{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1", ManifestDigest: w.manifest.Digest().String(), ScopeDigest: codecScope.String(),
			CoveredInputs: []byte(`{"runner":"v1"}`), PolicyRevisionID: "policy-1"}
		change(&r)
		return r
	}
	got, err := bindingFromRevision(row(func(*dbgen.ProposalRevision) {}))
	if err != nil || got.ManifestDigest() != w.manifest.Digest() || got.CoveredInputs()["runner"] != "v1" {
		t.Fatalf("bindingFromRevision of a valid row = %+v, %v", got, err)
	}
	cases := map[string]func(*dbgen.ProposalRevision){
		"covered inputs that are not an object":     func(r *dbgen.ProposalRevision) { r.CoveredInputs = []byte(`["runner"]`) },
		"covered inputs that are not valid JSON":    func(r *dbgen.ProposalRevision) { r.CoveredInputs = []byte(`{`) },
		"covered inputs with a non-text value":      func(r *dbgen.ProposalRevision) { r.CoveredInputs = []byte(`{"runner":1}`) },
		"a manifest digest that is not a digest":    func(r *dbgen.ProposalRevision) { r.ManifestDigest = "sha256:short" },
		"a scope digest that is not a digest":       func(r *dbgen.ProposalRevision) { r.ScopeDigest = "" },
		"an empty covered input value":              func(r *dbgen.ProposalRevision) { r.CoveredInputs = []byte(`{"runner":""}`) },
		"a revision that names no proposal":         func(r *dbgen.ProposalRevision) { r.ProposalID = "" },
		"a revision without its governing policy":   func(r *dbgen.ProposalRevision) { r.PolicyRevisionID = " " },
		"an expected canonical that is only spaces": func(r *dbgen.ProposalRevision) { r.ExpectedVersionID.String, r.ExpectedVersionID.Valid = " ", true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := bindingFromRevision(row(change)); err == nil {
				t.Fatal("bindingFromRevision accepted a damaged row")
			}
		})
	}
}

func TestDecodeRejectsDamagedPayloadPieces(t *testing.T) {
	w := newCodecWorld()
	binding := encodeBinding(w.binding)
	protected := encodeProtected(w.protected)
	command := must(encodeCommand(w.command))
	integration := must(encodeIntegration(w.integration))
	record := encodeRecord(w.record)
	if _, err := decodeBinding(binding); err != nil {
		t.Fatalf("a valid binding was rejected: %v", err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"binding with an unparsable manifest digest", func() error { b := binding; b.ManifestDigest = "nope"; _, err := decodeBinding(b); return err }},
		{"binding with an unparsable scope digest", func() error { b := binding; b.ScopeDigest = "nope"; _, err := decodeBinding(b); return err }},
		{"binding without a policy revision", func() error { b := binding; b.PolicyRevisionID = ""; _, err := decodeBinding(b); return err }},
		{"protected contract with an unparsable content digest", func() error {
			p := protected
			p.Manifest.Entries = []manifestEntryDTO{{Path: "tests/a.txt", Content: "nope"}}
			_, err := decodeProtected(p)
			return err
		}},
		{"protected contract with an unparsable scope digest", func() error { p := protected; p.ScopeDigest = "nope"; _, err := decodeProtected(p); return err }},
		{"protected contract with an empty covered input", func() error {
			p := protected
			p.CoveredInputs = map[string]string{"runner": ""}
			_, err := decodeProtected(p)
			return err
		}},
		{"principal with an unknown kind", func() error { _, err := decodePrincipal(principalDTO{ID: "owner", Kind: "robot"}); return err }},
		{"principal without an identity", func() error { _, err := decodePrincipal(principalDTO{Kind: "human"}); return err }},
		{"command with an unknown actor kind", func() error { c := command; c.Actor.Kind = "robot"; _, err := decodeCommand(c); return err }},
		{"command with an unknown action", func() error { c := command; c.Action = "delete"; _, err := decodeCommand(c); return err }},
		{"command with a zero order", func() error { c := command; c.Order = 0; _, err := decodeCommand(c); return err }},
		{"integration with an unknown kind", func() error { i := integration; i.Kind = "squash"; _, err := decodeIntegration(i); return err }},
		{"integration without a source", func() error { i := integration; i.Source = ""; _, err := decodeIntegration(i); return err }},
		{"record with a damaged binding", func() error { r := record; r.Binding.ScopeDigest = "nope"; _, err := decodeRecord(r); return err }},
		{"record without a version", func() error { r := record; r.VersionID = ""; _, err := decodeRecord(r); return err }},
		{"result with an unknown outcome", func() error { _, err := reconstituteResult(w.command, "maybe", "none"); return err }},
		{"result with an unknown reason", func() error { _, err := reconstituteResult(w.command, "approved", "because"); return err }},
		{"result whose outcome contradicts its reason", func() error { _, err := reconstituteResult(w.command, "approved", "unauthorized"); return err }},
	}
	for _, tt := range checks {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("a damaged piece was accepted")
			}
		})
	}
}

func TestDecodeReceiptRejectsDamagedStoredReceipts(t *testing.T) {
	w := newCodecWorld()
	consent := operationRow(t, governance.OperationConsent, &w.consent, nil)
	promotion := operationRow(t, governance.OperationPromote, nil, &w.promotion)
	edit := func(row dbgen.Operation, change func(map[string]any)) dbgen.Operation {
		var payload map[string]any
		if err := json.Unmarshal(row.Receipt, &payload); err != nil {
			t.Fatal(err)
		}
		change(payload)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		row.Receipt = raw
		return row
	}
	// at walks into a nested object of the receipt payload.
	at := func(payload map[string]any, path ...string) map[string]any {
		for _, key := range path {
			payload = payload[key].(map[string]any)
		}
		return payload
	}
	withKind := func(row dbgen.Operation, kind string) dbgen.Operation { row.Kind = kind; return row }
	withReceipt := func(row dbgen.Operation, raw string) dbgen.Operation { row.Receipt = []byte(raw); return row }

	cases := []struct {
		name string
		row  dbgen.Operation
	}{
		{"an unknown kind column", withKind(consent, "audit")},
		{"a receipt that is not JSON", withReceipt(consent, `{`)},
		{"a receipt with an unknown field", withReceipt(consent, `{"kind":"consent","extra":true}`)},
		{"a receipt kind that contradicts the kind column", withKind(consent, "promote")},
		{"a consent receipt without a body", withReceipt(consent, `{"kind":"consent"}`)},
		{"a consent receipt carrying both bodies", edit(consent, func(p map[string]any) { p["promotion"] = map[string]any{} })},
		{"a promotion receipt without a body", withReceipt(promotion, `{"kind":"promote"}`)},
		{"a promotion receipt carrying both bodies", edit(promotion, func(p map[string]any) { p["consent"] = map[string]any{} })},
		{"a consent body with an unknown actor kind", edit(consent, func(p map[string]any) { at(p, "consent", "command", "actor")["kind"] = "robot" })},
		{"a consent body with an unknown outcome", edit(consent, func(p map[string]any) { at(p, "consent")["outcome"] = "maybe" })},
		{"a promotion body with a damaged proposed contract", edit(promotion, func(p map[string]any) { at(p, "promotion", "request", "proposed")["scope_digest"] = "nope" })},
		{"a promotion body with an unknown integration kind", edit(promotion, func(p map[string]any) { at(p, "promotion", "request", "integration")["kind"] = "squash" })},
		{"a promotion body with a damaged binding", edit(promotion, func(p map[string]any) { at(p, "promotion", "binding")["manifest_digest"] = "nope" })},
		{"a promotion body with a damaged record", edit(promotion, func(p map[string]any) { at(p, "promotion", "record")["version_id"] = "" })},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeReceipt(tt.row)
			if !errors.Is(err, governance.ErrInvalidState) {
				t.Fatalf("decodeReceipt = %v; want ErrInvalidState", err)
			}
			if !strings.Contains(err.Error(), "operation") {
				t.Fatalf("the error does not name the operation: %v", err)
			}
		})
	}
}
