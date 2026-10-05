package postgres

import (
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The stable text codes of the schema (see the header of migration 00001).
// Every enumeration crosses the database boundary only through these switch
// tables, never as a Go integer value or a Go identifier name. A value without
// a code is rejected: a write fails with ErrInvalidRequest, a stored code that
// is not listed fails the read with ErrInvalidState.

var errUnknownCode = errors.New("unknown enumeration value or code")

func unknownValue(kind string, value any) error {
	return fmt.Errorf("%w: %s %v", errUnknownCode, kind, value)
}

func principalKindCode(kind contract.PrincipalKind) (string, error) {
	switch kind {
	case contract.Human:
		return "human", nil
	case contract.Agent:
		return "agent", nil
	case contract.Service:
		return "service", nil
	}
	return "", unknownValue("principal kind", kind)
}

func principalKindFromCode(code string) (contract.PrincipalKind, error) {
	switch code {
	case "human":
		return contract.Human, nil
	case "agent":
		return contract.Agent, nil
	case "service":
		return contract.Service, nil
	}
	return 0, unknownValue("principal kind code", code)
}

func consentActionCode(action contract.ConsentAction) (string, error) {
	switch action {
	case contract.ApproveConsent:
		return "approve", nil
	case contract.RevokeConsent:
		return "revoke", nil
	}
	return "", unknownValue("consent action", action)
}

func consentActionFromCode(code string) (contract.ConsentAction, error) {
	switch code {
	case "approve":
		return contract.ApproveConsent, nil
	case "revoke":
		return contract.RevokeConsent, nil
	}
	return 0, unknownValue("consent action code", code)
}

func consentOutcomeCode(outcome contract.ConsentOutcome) (string, error) {
	switch outcome {
	case contract.ConsentApproved:
		return "approved", nil
	case contract.ConsentRevoked:
		return "revoked", nil
	case contract.ConsentNoActiveApproval:
		return "no_active_approval", nil
	case contract.ConsentRejected:
		return "rejected", nil
	}
	return "", unknownValue("consent outcome", outcome)
}

func consentOutcomeFromCode(code string) (contract.ConsentOutcome, error) {
	switch code {
	case "approved":
		return contract.ConsentApproved, nil
	case "revoked":
		return contract.ConsentRevoked, nil
	case "no_active_approval":
		return contract.ConsentNoActiveApproval, nil
	case "rejected":
		return contract.ConsentRejected, nil
	}
	return 0, unknownValue("consent outcome code", code)
}

// consentReasonCode has no code for a command conflict: it is returned to the
// caller but never stored.
func consentReasonCode(reason contract.ConsentReason) (string, error) {
	switch reason {
	case contract.ConsentReasonNone:
		return "none", nil
	case contract.ConsentReasonUnauthorized:
		return "unauthorized", nil
	case contract.ConsentReasonUnknownRevision:
		return "unknown_revision", nil
	case contract.ConsentReasonSupersededRevision:
		return "superseded_revision", nil
	case contract.ConsentReasonContextMismatch:
		return "context_mismatch", nil
	case contract.ConsentReasonPolicyMismatch:
		return "policy_mismatch", nil
	case contract.ConsentReasonObsoleteCommand:
		return "obsolete_command", nil
	}
	return "", unknownValue("consent reason", reason)
}

func consentReasonFromCode(code string) (contract.ConsentReason, error) {
	switch code {
	case "none":
		return contract.ConsentReasonNone, nil
	case "unauthorized":
		return contract.ConsentReasonUnauthorized, nil
	case "unknown_revision":
		return contract.ConsentReasonUnknownRevision, nil
	case "superseded_revision":
		return contract.ConsentReasonSupersededRevision, nil
	case "context_mismatch":
		return contract.ConsentReasonContextMismatch, nil
	case "policy_mismatch":
		return contract.ConsentReasonPolicyMismatch, nil
	case "obsolete_command":
		return contract.ConsentReasonObsoleteCommand, nil
	}
	return 0, unknownValue("consent reason code", code)
}

func integrityOutcomeCode(outcome contract.IntegrityOutcome) (string, error) {
	switch outcome {
	case contract.IntegrityPassed:
		return "passed", nil
	case contract.IntegrityFailed:
		return "failed", nil
	case contract.IntegrityUnavailable:
		return "unavailable", nil
	}
	return "", unknownValue("integrity outcome", outcome)
}

func integrityOutcomeFromCode(code string) (contract.IntegrityOutcome, error) {
	switch code {
	case "passed":
		return contract.IntegrityPassed, nil
	case "failed":
		return contract.IntegrityFailed, nil
	case "unavailable":
		return contract.IntegrityUnavailable, nil
	}
	return 0, unknownValue("integrity outcome code", code)
}

func operationKindCode(kind governance.OperationKind) (string, error) {
	switch kind {
	case governance.OperationPromote:
		return "promote", nil
	case governance.OperationBootstrap:
		return "bootstrap", nil
	case governance.OperationCorrect:
		return "correct", nil
	case governance.OperationConsent:
		return "consent", nil
	}
	return "", unknownValue("operation kind", kind)
}

func operationKindFromCode(code string) (governance.OperationKind, error) {
	switch code {
	case "promote":
		return governance.OperationPromote, nil
	case "bootstrap":
		return governance.OperationBootstrap, nil
	case "correct":
		return governance.OperationCorrect, nil
	case "consent":
		return governance.OperationConsent, nil
	}
	return "", unknownValue("operation kind code", code)
}

func integrationKindCode(kind contract.IntegrationKind) (string, error) {
	switch kind {
	case contract.IntegrationMergedChange:
		return "merged_change", nil
	case contract.IntegrationExistingBaseline:
		return "existing_baseline", nil
	}
	return "", unknownValue("integration kind", kind)
}

func integrationKindFromCode(code string) (contract.IntegrationKind, error) {
	switch code {
	case "merged_change":
		return contract.IntegrationMergedChange, nil
	case "existing_baseline":
		return contract.IntegrationExistingBaseline, nil
	}
	return 0, unknownValue("integration kind code", code)
}
