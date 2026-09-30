package contract

import "errors"

var ErrInvalidHistoricalCanonical = errors.New("invalid historical canonical")

var ErrInvalidCorrection = errors.New("invalid correction")

type CorrectionInput struct {
	Promotion PromotionInput
	Target    HistoricalCanonical
}

func DecideCorrection(input CorrectionInput) (PromotionDecision, error) {
	promotion := input.Promotion
	promotion.CorrectsVersionID = input.Target.Version().ID()
	return DecidePromotion(promotion)
}

// HistoricalCanonical pairs a version with its immutable promotion provenance.
type HistoricalCanonical struct {
	version SuiteVersion
	record  PromotionRecord
}

func NewHistoricalCanonical(version SuiteVersion, record PromotionRecord) (HistoricalCanonical, error) {
	reference := record.Binding().Reference()
	if version.ID() == "" || record.IsZero() || version.ProjectID() != reference.ProjectID || version.SuiteID() != reference.SuiteID ||
		version.ID() != record.VersionID() || version.Manifest().Digest() != record.Binding().ManifestDigest() {
		return HistoricalCanonical{}, ErrInvalidHistoricalCanonical
	}
	return HistoricalCanonical{version: version, record: record}, nil
}

func (h HistoricalCanonical) Version() SuiteVersion   { return h.version }
func (h HistoricalCanonical) Record() PromotionRecord { return h.record }
func (h HistoricalCanonical) IsZero() bool            { return h.version.ID() == "" }
