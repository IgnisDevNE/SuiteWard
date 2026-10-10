package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	maxKindBytes    = 64
	maxKeyBytes     = 200
	maxPayloadBytes = 64 << 10
)

var kindSyntax = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// Validate reports ErrInvalidRequest when the job cannot be enqueued.
func (j Job) Validate() error {
	if err := validateKind(j.Kind); err != nil {
		return err
	}
	return validateObject("job args", j.Args)
}

// Validate reports ErrInvalidRequest when the message cannot be written.
func (m OutboxMessage) Validate() error {
	if strings.TrimSpace(m.Key) == "" || len(m.Key) > maxKeyBytes {
		return fmt.Errorf("%w: outbox key must be non-blank and at most %d bytes", ErrInvalidRequest, maxKeyBytes)
	}
	if err := validateKind(m.Kind); err != nil {
		return err
	}
	return validateObject("outbox payload", m.Payload)
}

func validateKind(kind string) error {
	if len(kind) > maxKindBytes || !kindSyntax.MatchString(kind) {
		return fmt.Errorf("%w: kind must match %s and be at most %d bytes", ErrInvalidRequest, kindSyntax, maxKindBytes)
	}
	return nil
}

func validateObject(what string, raw json.RawMessage) error {
	if len(raw) > maxPayloadBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidRequest, what, maxPayloadBytes)
	}
	if !json.Valid(raw) || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return fmt.Errorf("%w: %s must be a JSON object", ErrInvalidRequest, what)
	}
	return nil
}
