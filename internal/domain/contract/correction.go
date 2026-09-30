package contract

import "errors"

var ErrInvalidHistoricalCanonical = errors.New("invalid historical canonical")

// HistoricalCanonical pairs a version with its immutable promotion provenance.
type HistoricalCanonical struct {
	version SuiteVersion
	record  PromotionRecord
}

func NewHistoricalCanonical(version SuiteVersion, record PromotionRecord) (HistoricalCanonical, error) {
	return HistoricalCanonical{}, nil
}

func (h HistoricalCanonical) Version() SuiteVersion   { return h.version }
func (h HistoricalCanonical) Record() PromotionRecord { return h.record }
func (h HistoricalCanonical) IsZero() bool            { return h.version.ID() == "" }
