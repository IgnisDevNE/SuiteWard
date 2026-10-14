package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

var errNotImplemented = errors.New("postgres: not implemented")

var _ commands.Store = (*Store)(nil)

// Pending implements commands.Store.
func (s *Store) Pending(context.Context, time.Time, int) ([]commands.Candidate, error) {
	return nil, errNotImplemented
}

// Record implements commands.Store.
func (s *Store) Record(context.Context, commands.Entry, *governance.OutboxMessage) error {
	return errNotImplemented
}

// Instance implements commands.Store.
func (s *Store) Instance(context.Context) (string, error) { return "", errNotImplemented }
