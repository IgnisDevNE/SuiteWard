package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// StateCheckpoint carries supplied immutable facts for persistence. Restoration
// does not authenticate them or authorize a transition.
type StateCheckpoint struct {
	Canonical         CanonicalSnapshot
	Proposal          Proposal
	Policy            Policy
	Consent           Consent
	Scheduling        Schedule
	Assessment        IntegrityAssessment
	Integration       Integration
	History           HistoricalCanonical
	PromotionDecision PromotionDecision
	CommandResult     CommandResult
	Protected         ProtectedContract
	Command           Command
}

var ErrInvalidCheckpoint = errors.New("invalid domain checkpoint")

func EncodeStateCheckpoint(state StateCheckpoint) ([]byte, error) {
	data := checkpointData{Version: 1}
	if !state.Canonical.IsZero() {
		data.Canonical = canonicalDataOf(state.Canonical)
	}
	if !state.Proposal.IsZero() {
		data.Proposal = proposalDataOf(state.Proposal)
	}
	if state.Policy.RevisionID() != "" {
		data.Policy = policyDataOf(state.Policy)
	}
	if state.Consent.project != "" {
		data.Consent = consentDataOf(state.Consent)
	}
	if !state.Scheduling.IsZero() {
		data.Scheduling = scheduleDataOf(state.Scheduling)
	}
	if state.Assessment.Assurance() != 0 {
		data.Assessment = assessmentDataOf(state.Assessment)
	}
	if !state.Integration.IsZero() {
		data.Integration = integrationDataOf(state.Integration)
	}
	if !state.History.IsZero() {
		data.History = &historyData{Version: *versionDataOf(state.History.Version()), Record: *recordDataOf(state.History.Record())}
	}
	if state.PromotionDecision.Outcome() != 0 {
		data.Decision = decisionDataOf(state.PromotionDecision)
	}
	if state.CommandResult.Command().OperationID() != "" {
		data.Result = resultDataOf(state.CommandResult)
	}
	if !state.Protected.IsZero() {
		data.Protected = protectedDataOf(state.Protected)
	}
	if state.Command.OperationID() != "" {
		data.Command = commandDataOf(state.Command)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	if _, err := RestoreStateCheckpoint(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func RestoreStateCheckpoint(encoded []byte) (StateCheckpoint, error) {
	if err := validateCheckpointJSON(encoded); err != nil {
		return StateCheckpoint{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	var data checkpointData
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return StateCheckpoint{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	if data.Version != 1 {
		return StateCheckpoint{}, ErrInvalidCheckpoint
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return StateCheckpoint{}, ErrInvalidCheckpoint
	}
	state, err := data.restore()
	if err != nil {
		return StateCheckpoint{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	return state, nil
}

func validateCheckpointJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := checkpointJSONValue(decoder, false); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidCheckpoint
	}
	return nil
}

func checkpointJSONValue(decoder *json.Decoder, exactKeys bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	if delimiter == '{' {
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			rawName := key.(string)
			name := rawName
			if !exactKeys {
				for known := range keys {
					if strings.EqualFold(known, rawName) {
						return ErrInvalidCheckpoint
					}
				}
			}
			if keys[name] {
				return ErrInvalidCheckpoint
			}
			keys[name] = true
			// These dictionary keys are domain identities, unlike DTO field names.
			if err := checkpointJSONValue(decoder, strings.EqualFold(rawName, "Covered") || strings.EqualFold(rawName, "Operations")); err != nil {
				return err
			}
		}
	} else {
		for decoder.More() {
			if err := checkpointJSONValue(decoder, false); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token()
	return err
}
