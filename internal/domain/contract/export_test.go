package contract

// The effect of a promotion carries the Suite pointer the decision computed, and the baseline it was decided against, for
// the tests alone: the store advances the pointer itself under the Suite lock, and the baseline is the promotion
// record's binding.

func (e PromotionEffect) ExpectedCanonicalID() SuiteVersionID {
	return e.promotion.Binding().ExpectedCanonical()
}

func (e PromotionEffect) Suite() Suite { return e.suite }
