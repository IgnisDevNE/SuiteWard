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
	canonical := promotion.Context.Canonical
	if canonical.IsZero() || input.Target.IsZero() || promotion.Context.Proposal.IsZero() {
		return PromotionDecision{}, ErrInvalidCorrection
	}
	currentID, present := canonical.Suite().CurrentVersionID()
	target := input.Target.Version()
	if !present || target.ProjectID() != canonical.Suite().ProjectID() || target.SuiteID() != canonical.Suite().ID() ||
		promotion.NewVersionID == currentID || promotion.NewVersionID == target.ID() ||
		(promotion.CorrectsVersionID != "" && promotion.CorrectsVersionID != target.ID()) {
		return PromotionDecision{}, ErrInvalidCorrection
	}
	revision := promotion.Context.Proposal.Current()
	for _, historical := range []PromotionRecord{canonical.Record(), input.Target.Record()} {
		if revision.Binding().Reference().ProposalID == historical.Binding().Reference().ProposalID || revision.Carrier() == historical.Carrier() {
			return blockedPromotion(PromotionReasonCorrectionContextReused), nil
		}
	}
	promotion.CorrectsVersionID = target.ID()
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
