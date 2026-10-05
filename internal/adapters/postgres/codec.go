package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The JSON payloads below are immutable stored facts: manifests, covered
// inputs and operation receipts. They use private DTOs with explicit tags, so
// a refactoring of a domain type can never change what is already stored.
// Enumerations inside them use the same stable codes as the columns.

type referenceDTO struct {
	ProjectID  string `json:"project_id"`
	SuiteID    string `json:"suite_id"`
	ProposalID string `json:"proposal_id"`
	RevisionID string `json:"revision_id"`
}

type bindingDTO struct {
	Reference         referenceDTO      `json:"reference"`
	ExpectedCanonical string            `json:"expected_canonical"`
	ManifestDigest    string            `json:"manifest_digest"`
	ScopeDigest       string            `json:"scope_digest"`
	PolicyRevisionID  string            `json:"policy_revision_id"`
	CoveredInputs     map[string]string `json:"covered_inputs"`
}

type manifestEntryDTO struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type manifestDTO struct {
	Entries []manifestEntryDTO `json:"entries"`
}

type protectedDTO struct {
	Manifest      manifestDTO       `json:"manifest"`
	ScopeDigest   string            `json:"scope_digest"`
	CoveredInputs map[string]string `json:"covered_inputs"`
}

type principalDTO struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type commandDTO struct {
	OperationID     string       `json:"operation_id"`
	SourceCommandID string       `json:"source_command_id"`
	Actor           principalDTO `json:"actor"`
	Reference       referenceDTO `json:"reference"`
	Carrier         string       `json:"carrier"`
	Action          string       `json:"action"`
	Order           uint64       `json:"order"`
}

type consentReceiptDTO struct {
	Command                 commandDTO   `json:"command"`
	Outcome                 string       `json:"outcome"`
	Reason                  string       `json:"reason"`
	EvaluatedReference      referenceDTO `json:"evaluated_reference"`
	PolicyRevisionID        string       `json:"policy_revision_id"`
	CurrentApprovalEligible bool         `json:"current_approval_eligible"`
	PromotedVersionID       string       `json:"promoted_version_id"`
}

type integrationDTO struct {
	ProjectID string `json:"project_id"`
	TargetID  string `json:"target_id"`
	Source    string `json:"source"`
	Carrier   string `json:"carrier"`
	Kind      string `json:"kind"`
}

type promoteRequestDTO struct {
	OperationID      string         `json:"operation_id"`
	Reference        referenceDTO   `json:"reference"`
	Carrier          string         `json:"carrier"`
	Proposed         protectedDTO   `json:"proposed"`
	AssessmentSource string         `json:"assessment_source"`
	Integration      integrationDTO `json:"integration"`
	NewVersionID     string         `json:"new_version_id"`
	RecordedAt       time.Time      `json:"recorded_at"`
}

type recordDTO struct {
	OperationID       string     `json:"operation_id"`
	VersionID         string     `json:"version_id"`
	Binding           bindingDTO `json:"binding"`
	Carrier           string     `json:"carrier"`
	Source            string     `json:"source"`
	TargetID          string     `json:"target_id"`
	RecordedAt        time.Time  `json:"recorded_at"`
	CorrectsVersionID string     `json:"corrects_version_id"`
}

type promotionReceiptDTO struct {
	Request           promoteRequestDTO `json:"request"`
	CorrectsVersionID string            `json:"corrects_version_id"`
	Binding           bindingDTO        `json:"binding"`
	Record            recordDTO         `json:"record"`
}

// receiptDTO is the operations.receipt payload: exactly one of the two bodies,
// matching the kind that the operations row also records in its own column.
type receiptDTO struct {
	Kind      string               `json:"kind"`
	Consent   *consentReceiptDTO   `json:"consent,omitempty"`
	Promotion *promotionReceiptDTO `json:"promotion,omitempty"`
}

func invalidState(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", governance.ErrInvalidState, what, err)
}

func invalidRequest(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", governance.ErrInvalidRequest, what, err)
}

func encodeReference(reference contract.ProposalReference) referenceDTO {
	return referenceDTO{ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID), ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID)}
}

func decodeReference(dto referenceDTO) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: contract.ProjectID(dto.ProjectID), SuiteID: contract.SuiteID(dto.SuiteID), ProposalID: contract.ProposalID(dto.ProposalID), RevisionID: contract.ProposalRevisionID(dto.RevisionID)}
}

func nonNil(inputs map[string]string) map[string]string {
	if inputs == nil {
		return map[string]string{}
	}
	return inputs
}

func encodeBinding(binding contract.ApprovalBinding) bindingDTO {
	return bindingDTO{Reference: encodeReference(binding.Reference()), ExpectedCanonical: string(binding.ExpectedCanonical()), ManifestDigest: binding.ManifestDigest().String(),
		ScopeDigest: binding.ScopeDigest().String(), PolicyRevisionID: string(binding.PolicyRevisionID()), CoveredInputs: nonNil(binding.CoveredInputs())}
}

func decodeBinding(dto bindingDTO) (contract.ApprovalBinding, error) {
	manifest, err := artifact.ParseDigest(dto.ManifestDigest)
	if err != nil {
		return contract.ApprovalBinding{}, fmt.Errorf("manifest digest: %w", err)
	}
	scope, err := artifact.ParseDigest(dto.ScopeDigest)
	if err != nil {
		return contract.ApprovalBinding{}, fmt.Errorf("scope digest: %w", err)
	}
	return contract.NewApprovalBinding(contract.BindingInput{Reference: decodeReference(dto.Reference), ExpectedCanonical: contract.SuiteVersionID(dto.ExpectedCanonical),
		Manifest: manifest, Scope: scope, PolicyRevision: contract.PolicyRevisionID(dto.PolicyRevisionID), CoveredInputs: dto.CoveredInputs})
}

// bindingFromRevision rebuilds the approval binding that a proposal revision row holds.
func bindingFromRevision(row dbgen.ProposalRevision) (contract.ApprovalBinding, error) {
	var covered map[string]string
	if err := json.Unmarshal(row.CoveredInputs, &covered); err != nil {
		return contract.ApprovalBinding{}, fmt.Errorf("covered inputs: %w", err)
	}
	return decodeBinding(bindingDTO{
		Reference:         referenceDTO{ProjectID: row.ProjectID, SuiteID: row.SuiteID, ProposalID: row.ProposalID, RevisionID: row.RevisionID},
		ExpectedCanonical: row.ExpectedVersionID.String, ManifestDigest: row.ManifestDigest, ScopeDigest: row.ScopeDigest, PolicyRevisionID: row.PolicyRevisionID, CoveredInputs: covered,
	})
}

func encodeCoveredInputs(binding contract.ApprovalBinding) ([]byte, error) {
	return json.Marshal(nonNil(binding.CoveredInputs()))
}

func encodeManifest(manifest artifact.Manifest) ([]byte, error) {
	return json.Marshal(manifestToDTO(manifest))
}

func manifestToDTO(manifest artifact.Manifest) manifestDTO {
	dto := manifestDTO{Entries: []manifestEntryDTO{}}
	for _, entry := range manifest.Entries() {
		dto.Entries = append(dto.Entries, manifestEntryDTO{Path: entry.Path, Content: entry.Content.String()})
	}
	return dto
}

func manifestFromDTO(dto manifestDTO) (artifact.Manifest, error) {
	entries := make([]artifact.Entry, 0, len(dto.Entries))
	for _, entry := range dto.Entries {
		content, err := artifact.ParseDigest(entry.Content)
		if err != nil {
			return artifact.Manifest{}, fmt.Errorf("content digest of %q: %w", entry.Path, err)
		}
		entries = append(entries, artifact.Entry{Path: entry.Path, Content: content})
	}
	return artifact.NewManifest(entries)
}

// decodeManifest rebuilds a stored manifest and checks it against the digest
// column. The check hashes the small inventory only, never artifact content.
func decodeManifest(raw []byte, digest string) (artifact.Manifest, error) {
	var dto manifestDTO
	if err := decodeStrict(raw, &dto); err != nil {
		return artifact.Manifest{}, err
	}
	manifest, err := manifestFromDTO(dto)
	if err != nil {
		return artifact.Manifest{}, err
	}
	if manifest.Digest().String() != digest {
		return artifact.Manifest{}, fmt.Errorf("manifest hashes to %s but %s is stored", manifest.Digest(), digest)
	}
	return manifest, nil
}

func decodeStrict(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode stored JSON: %w", err)
	}
	return nil
}

func encodeProtected(protected contract.ProtectedContract) protectedDTO {
	return protectedDTO{Manifest: manifestToDTO(protected.Manifest()), ScopeDigest: protected.ScopeDigest().String(), CoveredInputs: nonNil(protected.CoveredInputs())}
}

func decodeProtected(dto protectedDTO) (contract.ProtectedContract, error) {
	manifest, err := manifestFromDTO(dto.Manifest)
	if err != nil {
		return contract.ProtectedContract{}, err
	}
	scope, err := artifact.ParseDigest(dto.ScopeDigest)
	if err != nil {
		return contract.ProtectedContract{}, fmt.Errorf("scope digest: %w", err)
	}
	return contract.NewProtectedContract(manifest, scope, dto.CoveredInputs)
}

func encodePrincipal(actor contract.Principal) (principalDTO, error) {
	kind, err := principalKindCode(actor.Kind())
	return principalDTO{ID: string(actor.ID()), Kind: kind}, err
}

func decodePrincipal(dto principalDTO) (contract.Principal, error) {
	kind, err := principalKindFromCode(dto.Kind)
	if err != nil {
		return contract.Principal{}, err
	}
	return contract.NewPrincipal(contract.PrincipalID(dto.ID), kind)
}

func encodeCommand(command contract.Command) (commandDTO, error) {
	actor, err := encodePrincipal(command.Actor())
	if err != nil {
		return commandDTO{}, err
	}
	action, err := consentActionCode(command.Action())
	if err != nil {
		return commandDTO{}, err
	}
	return commandDTO{OperationID: string(command.OperationID()), SourceCommandID: string(command.SourceCommandID()), Actor: actor,
		Reference: encodeReference(command.Reference()), Carrier: string(command.Carrier()), Action: action, Order: uint64(command.Order())}, nil
}

func decodeCommand(dto commandDTO) (contract.Command, error) {
	actor, err := decodePrincipal(dto.Actor)
	if err != nil {
		return contract.Command{}, err
	}
	action, err := consentActionFromCode(dto.Action)
	if err != nil {
		return contract.Command{}, err
	}
	return contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(dto.OperationID), SourceCommandID: contract.SourceCommandID(dto.SourceCommandID), Actor: actor,
		Reference: decodeReference(dto.Reference), Carrier: contract.ApprovalCarrierID(dto.Carrier), Action: action, Order: contract.CommandOrder(dto.Order)})
}

// reconstituteResult rebuilds a stored, non-duplicate command result from its codes.
func reconstituteResult(command contract.Command, outcomeCode, reasonCode string) (contract.CommandResult, error) {
	outcome, err := consentOutcomeFromCode(outcomeCode)
	if err != nil {
		return contract.CommandResult{}, err
	}
	reason, err := consentReasonFromCode(reasonCode)
	if err != nil {
		return contract.CommandResult{}, err
	}
	return contract.ReconstituteCommandResult(command, outcome, reason)
}

func encodeConsentReceipt(receipt governance.ConsentReceipt) (consentReceiptDTO, error) {
	if receipt.Result.Duplicate() {
		return consentReceiptDTO{}, errors.New("a receipt records the original result, not a duplicate")
	}
	command, err := encodeCommand(receipt.Result.Command())
	if err != nil {
		return consentReceiptDTO{}, err
	}
	outcome, err := consentOutcomeCode(receipt.Result.Outcome())
	if err != nil {
		return consentReceiptDTO{}, err
	}
	reason, err := consentReasonCode(receipt.Result.Reason())
	if err != nil {
		return consentReceiptDTO{}, err
	}
	return consentReceiptDTO{Command: command, Outcome: outcome, Reason: reason, EvaluatedReference: encodeReference(receipt.EvaluatedReference),
		PolicyRevisionID: string(receipt.PolicyRevisionID), CurrentApprovalEligible: receipt.CurrentApprovalEligible, PromotedVersionID: string(receipt.PromotedVersionID)}, nil
}

func decodeConsentReceipt(dto consentReceiptDTO) (governance.ConsentReceipt, error) {
	command, err := decodeCommand(dto.Command)
	if err != nil {
		return governance.ConsentReceipt{}, err
	}
	result, err := reconstituteResult(command, dto.Outcome, dto.Reason)
	if err != nil {
		return governance.ConsentReceipt{}, err
	}
	return governance.ConsentReceipt{Result: result, EvaluatedReference: decodeReference(dto.EvaluatedReference), PolicyRevisionID: contract.PolicyRevisionID(dto.PolicyRevisionID),
		CurrentApprovalEligible: dto.CurrentApprovalEligible, PromotedVersionID: contract.SuiteVersionID(dto.PromotedVersionID)}, nil
}

func encodeIntegration(integration contract.Integration) (integrationDTO, error) {
	kind, err := integrationKindCode(integration.Kind())
	return integrationDTO{ProjectID: string(integration.ProjectID()), TargetID: string(integration.Target()), Source: string(integration.Source()),
		Carrier: string(integration.Carrier()), Kind: kind}, err
}

func decodeIntegration(dto integrationDTO) (contract.Integration, error) {
	kind, err := integrationKindFromCode(dto.Kind)
	if err != nil {
		return contract.Integration{}, err
	}
	return contract.NewIntegration(contract.ProjectID(dto.ProjectID), contract.IntegrationTargetID(dto.TargetID), contract.SourceRevision(dto.Source), contract.ApprovalCarrierID(dto.Carrier), kind)
}

func encodeRecord(record contract.PromotionRecord) recordDTO {
	return recordDTO{OperationID: string(record.OperationID()), VersionID: string(record.VersionID()), Binding: encodeBinding(record.Binding()), Carrier: string(record.Carrier()),
		Source: string(record.Source()), TargetID: string(record.Target()), RecordedAt: record.RecordedAt().UTC(), CorrectsVersionID: string(record.CorrectsVersionID())}
}

func decodeRecord(dto recordDTO) (contract.PromotionRecord, error) {
	binding, err := decodeBinding(dto.Binding)
	if err != nil {
		return contract.PromotionRecord{}, err
	}
	return contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: contract.OperationID(dto.OperationID), VersionID: contract.SuiteVersionID(dto.VersionID), Binding: binding,
		Carrier: contract.ApprovalCarrierID(dto.Carrier), Source: contract.SourceRevision(dto.Source), Target: contract.IntegrationTargetID(dto.TargetID), RecordedAt: dto.RecordedAt.UTC(),
		CorrectsVersionID: contract.SuiteVersionID(dto.CorrectsVersionID)})
}

func encodePromotionReceipt(receipt governance.PromotionReceipt) (promotionReceiptDTO, error) {
	request := receipt.Identity.Request
	integration, err := encodeIntegration(request.Integration)
	if err != nil {
		return promotionReceiptDTO{}, err
	}
	return promotionReceiptDTO{
		Request: promoteRequestDTO{OperationID: string(request.OperationID), Reference: encodeReference(request.Reference), Carrier: string(request.Carrier), Proposed: encodeProtected(request.Proposed),
			AssessmentSource: string(request.AssessmentSource), Integration: integration, NewVersionID: string(request.NewVersionID), RecordedAt: request.RecordedAt.UTC()},
		CorrectsVersionID: string(receipt.Identity.CorrectsVersionID), Binding: encodeBinding(receipt.Identity.Binding), Record: encodeRecord(receipt.Record),
	}, nil
}

func decodePromotionReceipt(kind governance.OperationKind, dto promotionReceiptDTO) (governance.PromotionReceipt, error) {
	proposed, err := decodeProtected(dto.Request.Proposed)
	if err != nil {
		return governance.PromotionReceipt{}, err
	}
	integration, err := decodeIntegration(dto.Request.Integration)
	if err != nil {
		return governance.PromotionReceipt{}, err
	}
	binding, err := decodeBinding(dto.Binding)
	if err != nil {
		return governance.PromotionReceipt{}, err
	}
	record, err := decodeRecord(dto.Record)
	if err != nil {
		return governance.PromotionReceipt{}, err
	}
	request := governance.PromoteRequest{OperationID: contract.OperationID(dto.Request.OperationID), Reference: decodeReference(dto.Request.Reference), Carrier: contract.ApprovalCarrierID(dto.Request.Carrier),
		Proposed: proposed, AssessmentSource: contract.SourceRevision(dto.Request.AssessmentSource), Integration: integration, NewVersionID: contract.SuiteVersionID(dto.Request.NewVersionID),
		RecordedAt: dto.Request.RecordedAt.UTC()}
	return governance.PromotionReceipt{Identity: governance.PromotionIdentity{Kind: kind, Request: request, CorrectsVersionID: contract.SuiteVersionID(dto.CorrectsVersionID), Binding: binding}, Record: record}, nil
}

// encodeReceipt returns the operation kind code and the receipt payload.
func encodeReceipt(kind governance.OperationKind, consent *governance.ConsentReceipt, promotion *governance.PromotionReceipt) (string, []byte, error) {
	code, err := operationKindCode(kind)
	if err != nil {
		return "", nil, err
	}
	dto := receiptDTO{Kind: code}
	switch {
	case kind == governance.OperationConsent && consent != nil && promotion == nil:
		body, err := encodeConsentReceipt(*consent)
		if err != nil {
			return "", nil, err
		}
		dto.Consent = &body
	case kind != governance.OperationConsent && promotion != nil && consent == nil:
		body, err := encodePromotionReceipt(*promotion)
		if err != nil {
			return "", nil, err
		}
		dto.Promotion = &body
	default:
		return "", nil, fmt.Errorf("%s receipt does not carry exactly its own body", kind)
	}
	raw, err := json.Marshal(dto)
	return code, raw, err
}

// decodeReceipt rebuilds an operation receipt from its row.
func decodeReceipt(row dbgen.Operation) (governance.OperationReceipt, error) {
	kind, err := operationKindFromCode(row.Kind)
	if err != nil {
		return governance.OperationReceipt{}, invalidState("operation "+row.OperationID, err)
	}
	var dto receiptDTO
	if err := decodeStrict(row.Receipt, &dto); err != nil {
		return governance.OperationReceipt{}, invalidState("receipt of operation "+row.OperationID, err)
	}
	receipt := governance.OperationReceipt{Kind: kind, ProjectID: contract.ProjectID(row.ProjectID), SuiteID: contract.SuiteID(row.SuiteID)}
	switch {
	case dto.Kind != row.Kind:
		err = fmt.Errorf("receipt kind %q contradicts operation kind %q", dto.Kind, row.Kind)
	case kind == governance.OperationConsent && dto.Consent != nil && dto.Promotion == nil:
		var body governance.ConsentReceipt
		if body, err = decodeConsentReceipt(*dto.Consent); err == nil {
			receipt.Consent = &body
		}
	case kind != governance.OperationConsent && dto.Promotion != nil && dto.Consent == nil:
		var body governance.PromotionReceipt
		if body, err = decodePromotionReceipt(kind, *dto.Promotion); err == nil {
			receipt.Promotion = &body
		}
	default:
		err = errors.New("receipt does not carry exactly the body of its kind")
	}
	if err != nil {
		return governance.OperationReceipt{}, invalidState("receipt of operation "+row.OperationID, err)
	}
	return receipt, nil
}
