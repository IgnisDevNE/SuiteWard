package postgres

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// codeTable checks one enumeration in both directions: every value has its
// stable code, every code returns its value, and anything else is refused.
func codeTable[V comparable](t *testing.T, name string, pairs map[V]string, toCode func(V) (string, error), fromCode func(string) (V, error), unlisted []V) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		for value, code := range pairs {
			if got, err := toCode(value); err != nil || got != code {
				t.Errorf("code of %v = %q, %v; want %q", value, got, err, code)
			}
			if got, err := fromCode(code); err != nil || got != value {
				t.Errorf("value of %q = %v, %v; want %v", code, got, err, value)
			}
		}
		for _, value := range unlisted {
			if code, err := toCode(value); !errors.Is(err, errUnknownCode) || code != "" {
				t.Errorf("code of unlisted %v = %q, %v; want errUnknownCode and no code", value, code, err)
			}
		}
		for _, code := range []string{"", "unknown", "Human", " human"} {
			if _, listed := codeOf(pairs, code); listed {
				continue
			}
			if value, err := fromCode(code); !errors.Is(err, errUnknownCode) {
				t.Errorf("value of unlisted code %q = %v, %v; want errUnknownCode", code, value, err)
			}
		}
	})
}

func codeOf[V comparable](pairs map[V]string, code string) (V, bool) {
	for value, candidate := range pairs {
		if candidate == code {
			return value, true
		}
	}
	var zero V
	return zero, false
}

func TestEveryEnumerationHasStableCodes(t *testing.T) {
	codeTable(t, "principal kind", map[contract.PrincipalKind]string{contract.Human: "human", contract.Agent: "agent", contract.Service: "service"},
		principalKindCode, principalKindFromCode, []contract.PrincipalKind{0, 99})
	codeTable(t, "consent action", map[contract.ConsentAction]string{contract.ApproveConsent: "approve", contract.RevokeConsent: "revoke"},
		consentActionCode, consentActionFromCode, []contract.ConsentAction{0, 99})
	codeTable(t, "consent outcome", map[contract.ConsentOutcome]string{contract.ConsentApproved: "approved", contract.ConsentRevoked: "revoked",
		contract.ConsentNoActiveApproval: "no_active_approval", contract.ConsentRejected: "rejected"},
		consentOutcomeCode, consentOutcomeFromCode, []contract.ConsentOutcome{0, 99})
	// A command conflict is returned to the caller but never stored, so it has no code.
	codeTable(t, "consent reason", map[contract.ConsentReason]string{contract.ConsentReasonNone: "none", contract.ConsentReasonUnauthorized: "unauthorized",
		contract.ConsentReasonUnknownRevision: "unknown_revision", contract.ConsentReasonSupersededRevision: "superseded_revision",
		contract.ConsentReasonContextMismatch: "context_mismatch", contract.ConsentReasonPolicyMismatch: "policy_mismatch", contract.ConsentReasonObsoleteCommand: "obsolete_command"},
		consentReasonCode, consentReasonFromCode, []contract.ConsentReason{contract.ConsentReasonCommandConflict, 99})
	codeTable(t, "integrity outcome", map[contract.IntegrityOutcome]string{contract.IntegrityPassed: "passed", contract.IntegrityFailed: "failed", contract.IntegrityUnavailable: "unavailable"},
		integrityOutcomeCode, integrityOutcomeFromCode, []contract.IntegrityOutcome{0, 99})
	codeTable(t, "operation kind", map[governance.OperationKind]string{governance.OperationPromote: "promote", governance.OperationBootstrap: "bootstrap",
		governance.OperationCorrect: "correct", governance.OperationConsent: "consent"},
		operationKindCode, operationKindFromCode, []governance.OperationKind{"", "unknown", "PROMOTE"})
	codeTable(t, "integration kind", map[contract.IntegrationKind]string{contract.IntegrationMergedChange: "merged_change", contract.IntegrationExistingBaseline: "existing_baseline"},
		integrationKindCode, integrationKindFromCode, []contract.IntegrationKind{0, 99})
}
