package contract_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// TestPromotionReadinessDecidesSimultaneousFaultsInOrder gives each context two faults at once and pins the reason
// CheckPromotionReadiness names, so the order of its checks cannot change unnoticed: the proposal and its context, the
// inventory, the canonical, the policy, the approval, then the assessment (its assurance, then its source and binding,
// then its outcome).
func TestPromotionReadinessDecidesSimultaneousFaultsInOrder(t *testing.T) {
	blocked := func(reason contract.PromotionReason) func(*testing.T, contract.PromotionDecision, error) {
		return func(t *testing.T, decision contract.PromotionDecision, err error) {
			t.Helper()
			requirePromotionDecision(t, decision, err, contract.PromotionBlocked, reason)
		}
	}
	otherBinding := func(c *contract.PromotionContext) contract.ApprovalBinding {
		input := promotionCopyBindingInput(c.Proposal.Current().Binding())
		input.Reference.RevisionID = "other"
		return promotionBinding(t, input)
	}
	for _, tt := range []struct {
		name   string
		change func(*contract.PromotionContext)
		check  func(*testing.T, contract.PromotionDecision, error)
	}{
		{"an invalid structure before an unknown revision", func(c *contract.PromotionContext) {
			c.Policy = contract.Policy{}
			c.Reference.RevisionID = "unknown"
		}, func(t *testing.T, decision contract.PromotionDecision, err error) {
			if !errors.Is(err, contract.ErrInvalidPromotion) || decision.Outcome() != 0 {
				t.Fatalf("outcome=%v err=%v; want ErrInvalidPromotion", decision.Outcome(), err)
			}
		}},
		{"another carrier before an unknown revision", func(c *contract.PromotionContext) {
			c.Carrier = "other"
			c.Reference.RevisionID = "unknown"
		}, blocked(contract.PromotionReasonContextMismatch)},
		{"an unknown revision before another canonical", func(c *contract.PromotionContext) {
			c.Reference.RevisionID = "unknown"
			c.Canonical = promotionNewerCanonical(t)
		}, blocked(contract.PromotionReasonProposalNotCurrent)},
		{"another proposed contract before another canonical", func(c *contract.PromotionContext) {
			c.Proposed = promotionProtected(t, promotionManifest(t, "other"), "scope", c.Proposed.CoveredInputs())
			c.Canonical = promotionNewerCanonical(t)
		}, blocked(contract.PromotionReasonContextMismatch)},
		{"an empty inventory before another expected canonical", func(c *contract.PromotionContext) {
			*c = promotionContextWith(t, promotionAbsentCanonical(t), promotionProtected(t, promotionManifest(t), "scope", map[string]string{"runner": "v1"}))
			input := promotionCopyBindingInput(c.Proposal.Current().Binding())
			input.ExpectedCanonical = "v1"
			c.Proposal = promotionProposal(t, input, "candidate-source", c.Carrier)
		}, blocked(contract.PromotionReasonEmptyInventory)},
		{"another canonical before a policy of another project", func(c *contract.PromotionContext) {
			c.Canonical = promotionNewerCanonical(t)
			c.Policy = promotionPolicy(t, "foreign", "other", "owner")
		}, blocked(contract.PromotionReasonCanonicalChanged)},
		{"another canonical before a changed policy revision", func(c *contract.PromotionContext) {
			c.Canonical = promotionNewerCanonical(t)
			c.Policy = promotionPolicy(t, "project", "next-policy", "owner")
		}, blocked(contract.PromotionReasonCanonicalChanged)},
		{"a policy of another project before a changed policy revision", func(c *contract.PromotionContext) {
			c.Policy = promotionPolicy(t, "foreign", "other", "owner")
		}, blocked(contract.PromotionReasonContextMismatch)},
		{"a changed policy revision before a missing approval", func(c *contract.PromotionContext) {
			c.Policy = promotionPolicy(t, "project", "next-policy", "owner")
			c.Consent = contract.Consent{}
		}, blocked(contract.PromotionReasonPolicyChanged)},
		{"a missing approval before failed integrity", func(c *contract.PromotionContext) {
			c.Consent = contract.Consent{}
			c.Assessment = promotionAssessment(t, "integrated-source", c.Proposal.Current().Binding(), contract.IntegrityFailed)
		}, blocked(contract.PromotionReasonApprovalMissing)},
		{"a missing approval before another assessed source", func(c *contract.PromotionContext) {
			c.Consent = contract.Consent{}
			c.Assessment = promotionAssessment(t, "candidate-source", c.Proposal.Current().Binding(), contract.IntegrityPassed)
		}, blocked(contract.PromotionReasonApprovalMissing)},
		{"an absent assessment before another assessed source", func(c *contract.PromotionContext) {
			c.Assessment = contract.IntegrityAssessment{}
		}, blocked(contract.PromotionReasonIntegrityNotPassed)},
		{"another assessed source before failed integrity", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "candidate-source", c.Proposal.Current().Binding(), contract.IntegrityFailed)
		}, blocked(contract.PromotionReasonAssessmentMismatch)},
		{"another assessed binding before failed integrity", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "integrated-source", otherBinding(c), contract.IntegrityFailed)
		}, blocked(contract.PromotionReasonAssessmentMismatch)},
		{"another assessed binding before unavailable integrity", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "integrated-source", otherBinding(c), contract.IntegrityUnavailable)
		}, blocked(contract.PromotionReasonAssessmentMismatch)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := promotionContext(t)
			tt.change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			tt.check(t, decision, err)
		})
	}
}
