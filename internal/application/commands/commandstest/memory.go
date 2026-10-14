package commandstest

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

var errNotImplemented = errors.New("commandstest: not implemented")

// Memory is an in-memory commands.Store.
type Memory struct{}

var _ commands.Store = (*Memory)(nil)

// NewMemory returns a store that reads the enrolled projects from owners and the comments from observations.
func NewMemory(owners owner.Store, observations githubsync.Store) *Memory { return &Memory{} }

// Pending implements commands.Store.
func (m *Memory) Pending(context.Context, time.Time, int) ([]commands.Candidate, error) {
	return nil, errNotImplemented
}

// Record implements commands.Store.
func (m *Memory) Record(context.Context, commands.Entry, *governance.OutboxMessage) error {
	return errNotImplemented
}

// Instance implements commands.Store.
func (m *Memory) Instance(context.Context) (string, error) { return "", errNotImplemented }

// OutboxKeys returns the key of every committed acknowledgment, sorted.
func (m *Memory) OutboxKeys(context.Context) ([]string, error) { return nil, errNotImplemented }
