package contract

import (
	"errors"
	"time"
)

var ErrInvalidPromotionRecord = errors.New("invalid promotion record")

// PromotionRecordInput supplies immutable historical promotion facts.
type PromotionRecordInput struct {
	OperationID OperationID
	VersionID SuiteVersionID
	Binding ApprovalBinding
	Carrier ApprovalCarrierID
	Source SourceRevision
	Target IntegrationTargetID
	RecordedAt time.Time
	CorrectsVersionID SuiteVersionID
}

// PromotionRecord is supplied history, not proof that a promotion was authorized.
type PromotionRecord struct{}

func NewPromotionRecord(input PromotionRecordInput) (PromotionRecord, error) { return PromotionRecord{}, nil }
func (r PromotionRecord) OperationID() OperationID { return "" }
func (r PromotionRecord) VersionID() SuiteVersionID { return "" }
func (r PromotionRecord) Binding() ApprovalBinding { return ApprovalBinding{} }
func (r PromotionRecord) Carrier() ApprovalCarrierID { return "" }
func (r PromotionRecord) Source() SourceRevision { return "" }
func (r PromotionRecord) Target() IntegrationTargetID { return "" }
func (r PromotionRecord) RecordedAt() time.Time { return time.Time{} }
func (r PromotionRecord) CorrectsVersionID() SuiteVersionID { return "" }
func (r PromotionRecord) IsZero() bool { return true }
