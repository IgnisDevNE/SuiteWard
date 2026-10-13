package ownertest

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var errNotImplemented = errors.New("ownertest: not implemented")

// Memory is an in-memory owner.Store with the semantics of the persistence contract.
type Memory struct{}

var _ owner.Store = (*Memory)(nil)

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{} }

// AddSuite records that project has a Suite, which Enroll consults when the owner changes.
func (*Memory) AddSuite(contract.ProjectID) {}

// Enroll implements owner.Store.
func (*Memory) Enroll(context.Context, owner.Enrollment) error { return errNotImplemented }

// Current implements owner.Store.
func (*Memory) Current(context.Context) (owner.State, bool, error) {
	return owner.State{}, false, errNotImplemented
}

// ConsumeStep implements owner.Store.
func (*Memory) ConsumeStep(context.Context, int64, int64, time.Time) error { return errNotImplemented }

// RecordProtection implements owner.Store.
func (*Memory) RecordProtection(context.Context, owner.ProtectionResult) error {
	return errNotImplemented
}

// Protection implements owner.Store.
func (*Memory) Protection(context.Context) ([]owner.ProtectionResult, error) {
	return nil, errNotImplemented
}
