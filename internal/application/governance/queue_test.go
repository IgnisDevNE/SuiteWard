package governance_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

func objectOfSize(n int) json.RawMessage {
	// {"a":"xxx"} has 8 bytes of framing.
	return json.RawMessage(`{"a":"` + strings.Repeat("x", n-8) + `"}`)
}

func TestJobValidation(t *testing.T) {
	valid := json.RawMessage(`{"suite":"s1"}`)
	for name, job := range map[string]governance.Job{
		"minimal":          {Kind: "probe", Args: json.RawMessage(`{}`)},
		"dotted and digit": {Kind: "check.publish_2", Args: valid},
		"scheduled":        {Kind: "probe", Args: valid, ScheduledAt: time.Now().Add(time.Hour)},
		"longest kind":     {Kind: "a" + strings.Repeat("b", 63), Args: valid},
		"largest args":     {Kind: "probe", Args: objectOfSize(64 << 10)},
		"padded object":    {Kind: "probe", Args: json.RawMessage(" \n{} ")},
	} {
		t.Run("valid "+name, func(t *testing.T) {
			if err := job.Validate(); err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
		})
	}
	for name, job := range map[string]governance.Job{
		"empty kind":       {Args: valid},
		"upper case":       {Kind: "Probe", Args: valid},
		"leading digit":    {Kind: "1probe", Args: valid},
		"dash":             {Kind: "a-b", Args: valid},
		"overlong kind":    {Kind: "a" + strings.Repeat("b", 64), Args: valid},
		"missing args":     {Kind: "probe"},
		"null args":        {Kind: "probe", Args: json.RawMessage(`null`)},
		"array args":       {Kind: "probe", Args: json.RawMessage(`[]`)},
		"string args":      {Kind: "probe", Args: json.RawMessage(`"x"`)},
		"malformed args":   {Kind: "probe", Args: json.RawMessage(`{"a":`)},
		"trailing garbage": {Kind: "probe", Args: json.RawMessage(`{} x`)},
		"oversized args":   {Kind: "probe", Args: objectOfSize(64<<10 + 1)},
	} {
		t.Run("invalid "+name, func(t *testing.T) {
			if err := job.Validate(); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("Validate = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestOutboxMessageValidation(t *testing.T) {
	payload := json.RawMessage(`{"check":"c1"}`)
	for name, message := range map[string]governance.OutboxMessage{
		"minimal":     {Key: "k", Kind: "probe", Payload: json.RawMessage(`{}`)},
		"longest key": {Key: strings.Repeat("k", 200), Kind: "probe", Payload: payload},
		"unicode key": {Key: "clé-1", Kind: "check.publish", Payload: payload},
	} {
		t.Run("valid "+name, func(t *testing.T) {
			if err := message.Validate(); err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
		})
	}
	for name, message := range map[string]governance.OutboxMessage{
		"empty key":         {Kind: "probe", Payload: payload},
		"blank key":         {Key: " \t", Kind: "probe", Payload: payload},
		"overlong key":      {Key: strings.Repeat("k", 201), Kind: "probe", Payload: payload},
		"bad kind":          {Key: "k", Kind: "Probe", Payload: payload},
		"empty kind":        {Key: "k", Payload: payload},
		"missing payload":   {Key: "k", Kind: "probe"},
		"array payload":     {Key: "k", Kind: "probe", Payload: json.RawMessage(`[1]`)},
		"oversized payload": {Key: "k", Kind: "probe", Payload: objectOfSize(64<<10 + 1)},
	} {
		t.Run("invalid "+name, func(t *testing.T) {
			if err := message.Validate(); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("Validate = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
