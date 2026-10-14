package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var errNotImplemented = errors.New("not implemented")

// ackPayload is the payload of ack:<repository id>:<comment id>.
type ackPayload struct {
	commentTarget
	Project     contract.ProjectID   `json:"project,omitempty"`
	Suite       contract.SuiteID     `json:"suite,omitempty"`
	Operation   contract.OperationID `json:"operation,omitempty"`
	Disposition string               `json:"disposition,omitempty"`
	Comment     int64                `json:"comment"`
	Owner       string               `json:"owner"`
	Reason      string               `json:"reason,omitempty"`
	Reference   string               `json:"reference,omitempty"`
}

func newProcessedAck(target commentTarget, comment int64, ownerLogin string, project contract.ProjectID, suite contract.SuiteID, operation contract.OperationID) (governance.OutboxMessage, error) {
	return governance.OutboxMessage{}, nil
}

func newRejectedAck(target commentTarget, comment int64, ownerLogin string, disposition commands.Disposition, reason commandtext.Reason, reference string) (governance.OutboxMessage, error) {
	return governance.OutboxMessage{}, nil
}

// lazy resolves a value on first use and keeps it; a failure is returned and the next call tries again.
type lazy[T any] struct {
	resolve func(context.Context) (T, error)
	mu      sync.Mutex
	value   T
	done    bool
}

func newLazy[T any](resolve func(context.Context) (T, error)) *lazy[T] {
	return &lazy[T]{resolve: resolve}
}

func (l *lazy[T]) get(ctx context.Context) (T, error) {
	var zero T
	return zero, errNotImplemented
}

// botAccount is the numeric id of SuiteWard's bot account.
type botAccount struct{ *lazy[int64] }

func newBotAccount(resolve func(context.Context) (int64, error)) *botAccount {
	return &botAccount{newLazy(resolve)}
}

func (b *botAccount) ID(ctx context.Context) (int64, error) { return b.get(ctx) }

type commentWriter interface {
	PostComment(ctx context.Context, repo string, number int, body string) (int64, error)
	CommentsByAuthor(ctx context.Context, repo string, number int, authorID int64) ([]github.Comment, error)
}

type ownerSource interface {
	Current(ctx context.Context) (owner.State, bool, error)
}

type commentPublisher struct {
	writer   commentWriter
	bot      *botAccount
	instance *lazy[string]
	owners   ownerSource
	suites   governance.UnitOfWork
}

func (p *commentPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	return errNotImplemented
}

func ackOf(receipt governance.ConsentReceipt, ownerLogin string, comment int64) (commandtext.Ack, error) {
	return commandtext.Ack{}, errNotImplemented
}
