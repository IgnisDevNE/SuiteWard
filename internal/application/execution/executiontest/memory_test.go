package executiontest_test

import (
	"context"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

func TestMemoryConformance(t *testing.T) {
	executiontest.RunConformance(t, func(*testing.T) executiontest.Env {
		ledger := commandstest.NewMemory(ownertest.NewMemory(), githubsynctest.NewMemory())
		store, receipts := executiontest.NewMemory(ledger), executiontest.NewReceiptMemory()
		return executiontest.Env{Store: store, Receipts: receipts, Ledger: ledger, FailMessage: receipts.FailMessageAt, Gate: store.Gate, Unblocks: store.Unblocks, UnblockDecided: store.UnblockDecided,
			OutboxKeys: func(ctx context.Context) ([]string, error) { // the acknowledgments of the ledger and the messages of the Receipt check
				acknowledgments, err := ledger.OutboxKeys(ctx)
				if err != nil {
					return nil, err
				}
				messages, err := receipts.OutboxKeys(ctx)
				if err != nil {
					return nil, err
				}
				keys := slices.Concat(acknowledgments, messages)
				slices.Sort(keys)
				return keys, nil
			}}
	})
}
