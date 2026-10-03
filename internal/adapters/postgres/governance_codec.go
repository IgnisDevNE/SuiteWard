package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type authorityPayload struct {
	Version     int                          `json:"version"`
	Base        json.RawMessage              `json:"base"`
	Target      contract.IntegrationTargetID `json:"target"`
	Proposals   []json.RawMessage            `json:"proposals"`
	Assessments []json.RawMessage            `json:"assessments"`
}
type authorityState struct {
	canonical   contract.CanonicalSnapshot
	policy      contract.Policy
	scheduling  contract.Schedule
	target      contract.IntegrationTargetID
	proposals   map[contract.ProposalID]contract.Proposal
	consents    map[contract.ProposalID]contract.Consent
	assessments map[assessmentKey]contract.IntegrityAssessment
}
type assessmentKey struct {
	reference contract.ProposalReference
	source    contract.SourceRevision
}
type promotionPayload struct {
	Operation        contract.OperationID
	Reference        contract.ProposalReference
	Carrier          contract.ApprovalCarrierID
	AssessmentSource contract.SourceRevision
	NewVersion       contract.SuiteVersionID
	RecordedAt       time.Time
	BootstrapMode    contract.BootstrapMode
	Corrects         contract.SuiteVersionID
	Domain           json.RawMessage
}
type consentPayload struct {
	Domain             json.RawMessage
	EvaluatedReference contract.ProposalReference
	PolicyRevision     contract.PolicyRevisionID
	Eligible           bool
	PromotedVersion    contract.SuiteVersionID
}
type receiptPayload struct {
	Version   int
	Kind      governance.OperationKind
	Operation contract.OperationID
	Promotion *promotionPayload
	Consent   *consentPayload
}

func decodePayload(encoded []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return governance.ErrInvalidSnapshot
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return governance.ErrInvalidSnapshot
	}
	return nil
}
func encodeAuthority(state authorityState) ([]byte, error) {
	base, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{Canonical: state.canonical, Policy: state.policy, Scheduling: state.scheduling})
	if err != nil {
		return nil, err
	}
	payload := authorityPayload{Version: 1, Base: base, Target: state.target}
	for id, proposal := range state.proposals {
		encoded, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{Proposal: proposal, Consent: state.consents[id]})
		if err != nil {
			return nil, err
		}
		payload.Proposals = append(payload.Proposals, encoded)
	}
	for _, assessment := range state.assessments {
		encoded, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{Assessment: assessment})
		if err != nil {
			return nil, err
		}
		payload.Assessments = append(payload.Assessments, encoded)
	}
	return json.Marshal(payload)
}
func decodeAuthority(encoded []byte, project, suite, current, revision string) (authorityState, error) {
	var payload authorityPayload
	if err := decodePayload(encoded, &payload); err != nil {
		return authorityState{}, err
	}
	if payload.Version != 1 || strings.TrimSpace(string(payload.Target)) == "" {
		return authorityState{}, governance.ErrInvalidSnapshot
	}
	base, err := contract.RestoreStateCheckpoint(payload.Base)
	if err != nil {
		return authorityState{}, governance.ErrInvalidSnapshot
	}
	canonical := base.Canonical
	actualCurrent, _ := canonical.Suite().CurrentVersionID()
	r, err := strconv.ParseUint(revision, 10, 64)
	if err != nil || canonical.IsZero() || string(canonical.Suite().ProjectID()) != project || string(canonical.Suite().ID()) != suite || string(actualCurrent) != current || uint64(canonical.Suite().Revision()) != r || base.Policy.ProjectID() != canonical.Suite().ProjectID() || base.Policy.RevisionID() == "" || base.Scheduling.ProjectID() != canonical.Suite().ProjectID() || base.Scheduling.SuiteID() != canonical.Suite().ID() {
		return authorityState{}, governance.ErrInvalidSnapshot
	}
	state := authorityState{canonical: canonical, policy: base.Policy, scheduling: base.Scheduling, target: payload.Target, proposals: map[contract.ProposalID]contract.Proposal{}, consents: map[contract.ProposalID]contract.Consent{}, assessments: map[assessmentKey]contract.IntegrityAssessment{}}
	for _, encoded := range payload.Proposals {
		checkpoint, err := contract.RestoreStateCheckpoint(encoded)
		if err != nil || checkpoint.Proposal.IsZero() || reflect.DeepEqual(checkpoint.Consent, contract.Consent{}) {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
		ref := checkpoint.Proposal.Current().Binding().Reference()
		if string(ref.ProjectID) != project || string(ref.SuiteID) != suite || !state.proposals[ref.ProposalID].IsZero() {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
		state.proposals[ref.ProposalID] = checkpoint.Proposal
		state.consents[ref.ProposalID] = checkpoint.Consent
	}
	for _, encoded := range payload.Assessments {
		checkpoint, err := contract.RestoreStateCheckpoint(encoded)
		assessment := checkpoint.Assessment
		key := assessmentKey{assessment.Binding().Reference(), assessment.Source()}
		if err != nil || assessment.Assurance() == 0 || string(key.reference.ProjectID) != project || string(key.reference.SuiteID) != suite || state.assessments[key].Assurance() != 0 {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
		state.assessments[key] = assessment
	}
	for _, entry := range state.scheduling.Entries() {
		proposal := state.proposals[entry.ProposalID()]
		if proposal.IsZero() || proposal.Current().Carrier() != entry.Carrier() {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
	}
	for key, assessment := range state.assessments {
		proposal := state.proposals[key.reference.ProposalID]
		if proposal.IsZero() {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
		revision, err := proposal.Lookup(key.reference, proposal.Current().Carrier())
		if err != nil || !revision.Binding().Equal(assessment.Binding()) {
			return authorityState{}, governance.ErrInvalidSnapshot
		}
	}
	return state, nil
}
func encodeReceipt(receipt governance.OperationReceipt, operation contract.OperationID) ([]byte, error) {
	payload := receiptPayload{Version: 1, Kind: receipt.Kind, Operation: operation}
	if receipt.Kind == governance.OperationConsent {
		r := receipt.Consent
		domain, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{CommandResult: r.Result})
		if err != nil {
			return nil, err
		}
		payload.Consent = &consentPayload{domain, r.EvaluatedReference, r.PolicyRevisionID, r.CurrentApprovalEligible, r.PromotedVersionID}
	} else {
		r := receipt.Promotion
		i := r.Identity
		request := i.Request
		domain, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{PromotionDecision: r.Decision, Protected: request.Proposed, Integration: request.Integration})
		if err != nil {
			return nil, err
		}
		payload.Promotion = &promotionPayload{request.OperationID, request.Reference, request.Carrier, request.AssessmentSource, request.NewVersionID, request.RecordedAt, i.BootstrapMode, i.CorrectsVersionID, domain}
	}
	return json.Marshal(payload)
}
func decodeReceipt(encoded []byte, kind int16, project, suite, operation string) (governance.OperationReceipt, error) {
	var payload receiptPayload
	if err := decodePayload(encoded, &payload); err != nil {
		return governance.OperationReceipt{}, err
	}
	if payload.Version != 1 || int16(payload.Kind) != kind || string(payload.Operation) != operation || strings.TrimSpace(operation) == "" {
		return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
	}
	receipt := governance.OperationReceipt{Kind: payload.Kind}
	if payload.Kind == governance.OperationConsent {
		if payload.Consent == nil || payload.Promotion != nil {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		p := payload.Consent
		domain, err := contract.RestoreStateCheckpoint(p.Domain)
		if err != nil {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		result := domain.CommandResult
		reference := result.Command().Reference()
		if result.Command().OperationID() == "" || result.Duplicate() || string(reference.ProjectID) != project || string(reference.SuiteID) != suite || !sameAggregate(reference, p.EvaluatedReference) || p.EvaluatedReference.RevisionID == "" || strings.TrimSpace(string(p.PolicyRevision)) == "" {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		receipt.Consent = governance.ConsentReceipt{Result: result, EvaluatedReference: p.EvaluatedReference, PolicyRevisionID: p.PolicyRevision, CurrentApprovalEligible: p.Eligible, PromotedVersionID: p.PromotedVersion}
		return receipt, nil
	}
	if payload.Kind < governance.OperationPromote || payload.Kind > governance.OperationCorrect || payload.Promotion == nil || payload.Consent != nil {
		return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
	}
	p := payload.Promotion
	domain, err := contract.RestoreStateCheckpoint(p.Domain)
	if err != nil {
		return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
	}
	effect, present := domain.PromotionDecision.Effect()
	if !present || string(p.Reference.ProjectID) != project || string(p.Reference.SuiteID) != suite {
		return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
	}
	identity := governance.PromotionIdentity{Kind: payload.Kind, Request: governance.PromoteRequest{OperationID: p.Operation, Reference: p.Reference, Carrier: p.Carrier, Proposed: domain.Protected, AssessmentSource: p.AssessmentSource, Integration: domain.Integration, NewVersionID: p.NewVersion, RecordedAt: p.RecordedAt}, BootstrapMode: p.BootstrapMode, CorrectsVersionID: p.Corrects, Binding: effect.Promotion().Binding()}
	receipt.Promotion = governance.PromotionReceipt{Identity: identity, Decision: domain.PromotionDecision}
	return receipt, nil
}
func sameAggregate(a, b contract.ProposalReference) bool {
	return a.ProjectID == b.ProjectID && a.SuiteID == b.SuiteID && a.ProposalID == b.ProposalID
}
