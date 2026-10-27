package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// receiptLedger is the part of the ledger of the Receipt check that the publisher reads.
type receiptLedger interface {
	Current(ctx context.Context, repositoryID int64, pull int) (execution.ReceiptRecord, bool, error)
}

// receiptCheckPublisher is the river.Publisher of the github_receipt_check kind.
type receiptCheckPublisher struct {
	runs     checkRunner
	ledger   receiptLedger
	instance *lazy[string] // commands.Store.Instance, read once
	appID    int64         // the App that posts, whose runs the readback lists
	logger   *slog.Logger
}

// publish delivers one github_receipt_check message.
func (p *receiptCheckPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	return nil
}
