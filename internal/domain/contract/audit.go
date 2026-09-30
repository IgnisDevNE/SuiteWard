package contract

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidPromotionRecord = errors.New("invalid promotion record")

// PromotionRecordInput supplies immutable historical promotion facts.
type PromotionRecordInput struct {
	OperationID       OperationID
	VersionID         SuiteVersionID
	Binding           ApprovalBinding
	Carrier           ApprovalCarrierID
	Source            SourceRevision
	Target            IntegrationTargetID
	RecordedAt        time.Time
	CorrectsVersionID SuiteVersionID
}

// PromotionRecord is supplied history, not proof that a promotion was authorized.
type PromotionRecord struct{ input PromotionRecordInput }

func NewPromotionRecord(input PromotionRecordInput) (PromotionRecord, error) {
	if strings.TrimSpace(string(input.OperationID)) == "" || strings.TrimSpace(string(input.VersionID)) == "" || input.Binding.IsZero() ||
		strings.TrimSpace(string(input.Carrier)) == "" || strings.TrimSpace(string(input.Source)) == "" || strings.TrimSpace(string(input.Target)) == "" || input.RecordedAt.IsZero() ||
		input.VersionID == input.Binding.ExpectedCanonical() {
		return PromotionRecord{}, ErrInvalidPromotionRecord
	}
	if input.CorrectsVersionID != "" && (strings.TrimSpace(string(input.CorrectsVersionID)) == "" || input.CorrectsVersionID == input.VersionID) {
		return PromotionRecord{}, ErrInvalidPromotionRecord
	}
	return PromotionRecord{input: input}, nil
}
func (r PromotionRecord) OperationID() OperationID          { return r.input.OperationID }
func (r PromotionRecord) VersionID() SuiteVersionID         { return r.input.VersionID }
func (r PromotionRecord) Binding() ApprovalBinding          { return r.input.Binding }
func (r PromotionRecord) Carrier() ApprovalCarrierID        { return r.input.Carrier }
func (r PromotionRecord) Source() SourceRevision            { return r.input.Source }
func (r PromotionRecord) Target() IntegrationTargetID       { return r.input.Target }
func (r PromotionRecord) RecordedAt() time.Time             { return r.input.RecordedAt }
func (r PromotionRecord) CorrectsVersionID() SuiteVersionID { return r.input.CorrectsVersionID }
func (r PromotionRecord) IsZero() bool                      { return r.input.Binding.IsZero() }

// AuditEvent describes a proposed canonical-promotion audit fact.
type AuditEvent struct{ promotion PromotionRecord }

func (a AuditEvent) Promotion() PromotionRecord { return a.promotion }
func (a AuditEvent) IsZero() bool               { return a.promotion.IsZero() }

// PublicationIntent is a pending acknowledgment, not proof of remote delivery.
type PublicationIntent struct{ promotion PromotionRecord }

func (p PublicationIntent) Promotion() PromotionRecord { return p.promotion }
func (p PublicationIntent) IsZero() bool               { return p.promotion.IsZero() }
