// Package checkstest provides an in-memory checks.Store and the conformance
// suite that every checks.Store must pass.
package checkstest

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Memory is an in-memory checks.Store with the semantics of the persistence contract.
type Memory struct{}

var _ checks.Store = (*Memory)(nil)

var errNotImplemented = errors.New("checkstest: not implemented")

// NewMemory returns an empty ledger.
func NewMemory() *Memory { return &Memory{} }

// Current implements checks.Store.
func (m *Memory) Current(context.Context, int64, int) (checks.Record, bool, error) {
	return checks.Record{}, false, errNotImplemented
}

// Publish implements checks.Store.
func (m *Memory) Publish(context.Context, checks.Result, time.Time, func(int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
	return checks.Record{}, false, errNotImplemented
}

// OutboxKeys returns the key of every committed message, sorted.
func (m *Memory) OutboxKeys(context.Context) ([]string, error) { return nil, errNotImplemented }

// FailMessage marks a pending message as failed, as the relay does once its attempts are used up.
func (m *Memory) FailMessage(context.Context, string) error { return errNotImplemented }
