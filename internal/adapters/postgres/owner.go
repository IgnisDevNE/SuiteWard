package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

var _ owner.Store = (*Store)(nil)

var errOwnerNotImplemented = errors.New("PostgreSQL owner store is not implemented")

// Enroll implements owner.Store.
func (*Store) Enroll(context.Context, owner.Enrollment) error { return errOwnerNotImplemented }

// Current implements owner.Store.
func (*Store) Current(context.Context) (owner.State, bool, error) {
	return owner.State{}, false, errOwnerNotImplemented
}

// ConsumeStep implements owner.Store.
func (*Store) ConsumeStep(context.Context, int64, int64, time.Time) error {
	return errOwnerNotImplemented
}

// RecordProtection implements owner.Store.
func (*Store) RecordProtection(context.Context, owner.ProtectionResult) error {
	return errOwnerNotImplemented
}

// Protection implements owner.Store.
func (*Store) Protection(context.Context) ([]owner.ProtectionResult, error) {
	return nil, errOwnerNotImplemented
}
