package postgres

import (
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The stable text codes of the schema (see the header of migration 00001).
// Every enumeration crosses the database boundary only through these tables,
// never as a Go integer value or a Go identifier name. A value without a code
// is rejected: a write fails with ErrInvalidRequest, a stored code that is not
// listed fails the read with ErrInvalidState.

var errUnknownCode = errors.New("unknown enumeration value or code")

func unknownValue(kind string, value any) error {
	return fmt.Errorf("%w: %s %v", errUnknownCode, kind, value)
}

// enumeration returns the two directions of one table of codes.
func enumeration[V comparable](kind string, codes map[V]string) (toCode func(V) (string, error), fromCode func(string) (V, error)) {
	values := make(map[string]V, len(codes))
	for value, code := range codes {
		values[code] = value
	}
	toCode = func(value V) (string, error) {
		code, listed := codes[value]
		if !listed {
			return "", unknownValue(kind, value)
		}
		return code, nil
	}
	fromCode = func(code string) (V, error) {
		value, listed := values[code]
		if !listed {
			return value, unknownValue(kind+" code", code)
		}
		return value, nil
	}
	return toCode, fromCode
}

var (
	principalKindCode, principalKindFromCode = enumeration("principal kind", map[contract.PrincipalKind]string{
		contract.Human: "human", contract.Agent: "agent", contract.Service: "service"})
	consentActionCode, consentActionFromCode = enumeration("consent action", map[contract.ConsentAction]string{
		contract.ApproveConsent: "approve", contract.RevokeConsent: "revoke"})
	consentOutcomeCode, consentOutcomeFromCode = enumeration("consent outcome", map[contract.ConsentOutcome]string{
		contract.ConsentApproved: "approved", contract.ConsentRevoked: "revoked", contract.ConsentNoActiveApproval: "no_active_approval", contract.ConsentRejected: "rejected"})
	// consentReasonCode has no code for a command conflict: it is returned to the caller but never stored.
	consentReasonCode, consentReasonFromCode = enumeration("consent reason", map[contract.ConsentReason]string{
		contract.ConsentReasonNone: "none", contract.ConsentReasonUnauthorized: "unauthorized", contract.ConsentReasonUnknownRevision: "unknown_revision",
		contract.ConsentReasonSupersededRevision: "superseded_revision", contract.ConsentReasonContextMismatch: "context_mismatch",
		contract.ConsentReasonPolicyMismatch: "policy_mismatch", contract.ConsentReasonObsoleteCommand: "obsolete_command"})
	integrityOutcomeCode, integrityOutcomeFromCode = enumeration("integrity outcome", map[contract.IntegrityOutcome]string{
		contract.IntegrityPassed: "passed", contract.IntegrityFailed: "failed", contract.IntegrityUnavailable: "unavailable"})
	operationKindCode, operationKindFromCode = enumeration("operation kind", map[governance.OperationKind]string{
		governance.OperationPromote: "promote", governance.OperationBootstrap: "bootstrap", governance.OperationCorrect: "correct", governance.OperationConsent: "consent"})
	integrationKindCode, integrationKindFromCode = enumeration("integration kind", map[contract.IntegrationKind]string{
		contract.IntegrationMergedChange: "merged_change", contract.IntegrationExistingBaseline: "existing_baseline"})
)
