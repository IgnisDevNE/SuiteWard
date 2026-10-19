package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// receiptFailsSuites reads every receipt as a store error.
type receiptFailsSuites struct{ governance.UnitOfWork }

func (s *receiptFailsSuites) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return s.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error { return fn(ctx, receiptFailsTx{tx}) })
}

type receiptFailsTx struct{ governance.Tx }

func (receiptFailsTx) Receipt(context.Context, contract.OperationID) (governance.OperationReceipt, bool, error) {
	return governance.OperationReceipt{}, false, errBootStore
}

// listsDisposition answers the latest command of one pull with an entry of a disposition that the ledger never lists.
type listsDisposition struct {
	ownerCommands
	pull        int
	disposition commands.Disposition
	at          time.Time
}

func (l listsDisposition) LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID, before int64) (commands.Entry, bool, error) {
	if pull != l.pull {
		return l.ownerCommands.LatestOwnerCommand(ctx, repositoryID, pull, ownerID, before)
	}
	return commands.Entry{RepositoryID: repositoryID, CommentID: 301, PullNumber: pull, AuthorID: ownerID, CreatedAt: l.at, Disposition: l.disposition}, true, nil
}

// A command whose receipt cannot be read says nothing about the owner, so the run fails and the check stays as it was until the next run reads it.
func TestChecksFailTheRunWhenTheReceiptOfAProcessedCommandCannotBeRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	if outcome := w.consent(contract.ApproveConsent, 301, 2, 9, 5); outcome != contract.ConsentRejected { // P2-R9 does not exist
		t.Fatalf("outcome %v, want rejected", outcome)
	}
	w.check.suites = &receiptFailsSuites{UnitOfWork: w.suites}

	err := w.runChecks()

	if !errors.Is(err, errBootStore) || !strings.Contains(err.Error(), "read the receipt of comment 301") {
		t.Fatalf("run error %v, want the store error of the receipt of comment 301", err)
	}
	w.want(5, checks.InProgress, "approval-missing", 1)

	w.check.suites = w.suites
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 1)
}

// The ledger lists only the commands that can set or clear the check; one of another disposition is a defect of the ledger and fails the run
// instead of being read as a command.
func TestChecksFailTheRunOnACommandThatCannotSetOrClearTheCheck(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.check.commands = listsDisposition{ownerCommands: w.cmds, pull: 5, disposition: commands.Skipped, at: w.clock.Add(time.Hour)}

	err := w.runChecks()

	if err == nil || !strings.Contains(err.Error(), `the owner command 301 has the disposition "skipped", which cannot set or clear a check`) {
		t.Fatalf("run error %v, want the unlisted disposition", err)
	}
	w.want(5, checks.InProgress, "approval-missing", 1)
}
