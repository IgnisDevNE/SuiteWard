package contract_test

import (
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func promotionRecordInput(t *testing.T) contract.PromotionRecordInput {
	t.Helper()
	input := contract.BindingInput{
		Reference:         contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "prior-proposal", RevisionID: "r1"},
		ExpectedCanonical: "v0", Manifest: promotionManifest(t, "old").Digest(),
		Scope:          promotionProtected(t, promotionManifest(t, "old"), "scope", nil).ScopeDigest(),
		PolicyRevision: "policy", CoveredInputs: map[string]string{"runner": "v1"},
	}
	binding, err := contract.NewApprovalBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	return contract.PromotionRecordInput{OperationID: "prior-operation", VersionID: "v1", Binding: binding, Carrier: "prior-carrier", Source: "integrated-old", Target: "main", RecordedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), CorrectsVersionID: "v0"}
}

func promotionRecord(t *testing.T, input contract.PromotionRecordInput) contract.PromotionRecord {
	t.Helper()
	record, err := contract.NewPromotionRecord(input)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPromotionRecordPreservesExactHistory(t *testing.T) {
	input := promotionRecordInput(t)
	record := promotionRecord(t, input)
	if record.IsZero() || record.OperationID() != input.OperationID || record.VersionID() != input.VersionID || !record.Binding().Equal(input.Binding) || record.Carrier() != input.Carrier || record.Source() != input.Source || record.Target() != input.Target || record.RecordedAt() != input.RecordedAt || record.CorrectsVersionID() != input.CorrectsVersionID {
		t.Fatal("promotion record lost its exact historical identity, binding or attribution")
	}
	input.VersionID = "changed"
	context := record.Binding().CoveredInputs()
	context["runner"] = "changed"
	if record.VersionID() != "v1" || record.Binding().CoveredInputs()["runner"] != "v1" {
		t.Fatal("record aliases mutable input")
	}
	input = promotionRecordInput(t)
	input.CorrectsVersionID = ""
	if promotionRecord(t, input).CorrectsVersionID() != "" {
		t.Fatal("optional correction attribution was fabricated")
	}
	if !(contract.PromotionRecord{}).IsZero() {
		t.Fatal("zero record is present")
	}
}

func TestPromotionRecordRejectsInconsistentHistory(t *testing.T) {
	for name, change := range map[string]func(*contract.PromotionRecordInput){
		"operation absent": func(i *contract.PromotionRecordInput) { i.OperationID = "" },
		"operation blank":  func(i *contract.PromotionRecordInput) { i.OperationID = " \t" },
		"version absent":   func(i *contract.PromotionRecordInput) { i.VersionID = "" },
		"version blank":    func(i *contract.PromotionRecordInput) { i.VersionID = " \t" },
		"reused baseline":  func(i *contract.PromotionRecordInput) { i.VersionID = i.Binding.ExpectedCanonical() },
		"binding absent":   func(i *contract.PromotionRecordInput) { i.Binding = contract.ApprovalBinding{} },
		"carrier absent":   func(i *contract.PromotionRecordInput) { i.Carrier = "" },
		"carrier blank":    func(i *contract.PromotionRecordInput) { i.Carrier = "\n" },
		"source absent":    func(i *contract.PromotionRecordInput) { i.Source = "" },
		"source blank":     func(i *contract.PromotionRecordInput) { i.Source = "\t" },
		"target absent":    func(i *contract.PromotionRecordInput) { i.Target = "" },
		"target blank":     func(i *contract.PromotionRecordInput) { i.Target = " " },
		"time absent":      func(i *contract.PromotionRecordInput) { i.RecordedAt = time.Time{} },
		"correction blank": func(i *contract.PromotionRecordInput) { i.CorrectsVersionID = " \t" },
		"self correction":  func(i *contract.PromotionRecordInput) { i.CorrectsVersionID = i.VersionID },
	} {
		t.Run(name, func(t *testing.T) {
			input := promotionRecordInput(t)
			change(&input)
			got, err := contract.NewPromotionRecord(input)
			if !errors.Is(err, contract.ErrInvalidPromotionRecord) || !got.IsZero() {
				t.Fatalf("inconsistent record accepted: err=%v", err)
			}
		})
	}
}
