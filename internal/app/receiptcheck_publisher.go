package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The github_receipt_check publisher (docs/contracts/runtime.md, "The SuiteWard / Receipt check", "Outbox kind github_receipt_check"). The checks job decides the state and
// writes the message through the ledger of the check; the publisher posts the run of a message that is still current, and only of a state that GitHub has a conclusion for:
// a receipt that does not apply is never sent (domain.ConclusionNone).

// receiptCheckName is the name of the check run of the receipt. It is never required.
const receiptCheckName = github.ReceiptCheckName

// receiptCheckStates are the states of a Receipt check message, and what each is as a check run. No other state is a conclusion GitHub has.
var receiptCheckStates = map[string]commandtext.CheckState{
	string(execution.CheckInProgress): commandtext.CheckInProgress, string(execution.CheckSuccess): commandtext.CheckSuccess,
	string(execution.CheckFailure): commandtext.CheckFailure, string(execution.CheckNeutral): commandtext.CheckNeutral,
}

// receiptCountKeys are the keys of the counts of the payload, every one of which is always present.
var receiptCountKeys = []string{"detects", "inert", "newApi", "fails", "missing", "flaky", "skipped", "excluded", "markers", "unparsed"}

// check validates the fields of the payload and returns what its output states. It does not render, so a message that is dropped as stale never meets a reason that no
// longer exists.
func (p receiptCheckPayload) check(key string) (commandtext.ReceiptCheck, error) {
	state, tier := receiptCheckStates[p.State], checkTiers[p.Tier]
	if err := p.validate(); err != nil {
		return commandtext.ReceiptCheck{}, keyError(receiptCheckKind, key, err.Error())
	}
	switch {
	case !headPattern.MatchString(p.HeadSHA):
		return commandtext.ReceiptCheck{}, keyError(receiptCheckKind, key, "the head is not 40 lowercase hexadecimal digits")
	case p.Generation < 1:
		return commandtext.ReceiptCheck{}, keyError(receiptCheckKind, key, "the generation must be positive")
	case state == 0 || tier == 0:
		return commandtext.ReceiptCheck{}, keyError(receiptCheckKind, key, "the state or the tier is unknown")
	}
	c := commandtext.ReceiptCheck{State: state, Reason: p.Reason, Tier: tier, Unavailable: p.Unavailable, Unblocked: p.Unblocked, Markers: p.Counts.Markers,
		Counts: domain.ReceiptCounts{Detects: p.Counts.Detects, Inert: p.Counts.Inert, NewAPI: p.Counts.NewAPI, Fails: p.Counts.Fails, Missing: p.Counts.Missing, Flaky: p.Counts.Flaky,
			Skipped: p.Counts.Skipped, Excluded: p.Counts.Excluded, Unparsed: p.Counts.Unparsed}}
	if p.Reference != "" {
		var ok bool
		if c.Proposal, c.Revision, ok = commandtext.ParseReference(p.Reference); !ok {
			return commandtext.ReceiptCheck{}, keyError(receiptCheckKind, key, "the reference is not P<n>-R<m>")
		}
	}
	return c, nil
}

// output validates the payload and renders the title and the summary of its check run; key only names the message in an error, which never quotes the payload.
func (p receiptCheckPayload) output(key string) (title, summary string, err error) {
	c, err := p.check(key)
	if err != nil {
		return "", "", err
	}
	_, err = render(receiptCheckKind, key, func() string { // a reason or a combination commandtext does not know panics there
		title, summary = commandtext.ReceiptCheckSummary(c)
		return summary
	})
	return title, summary, err
}

// decodeReceiptCheck reads the payload strictly. Every key of the counts is always present, which strict decoding cannot tell from a count of zero.
func decodeReceiptCheck(key string, payload json.RawMessage) (receiptCheckPayload, error) {
	p, err := decodePayload[receiptCheckPayload](receiptCheckKind, key, payload)
	if err != nil {
		return p, err
	}
	var raw struct {
		Counts map[string]json.RawMessage `json:"counts"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil { // decodePayload read it: only the shape of the counts is left to see
		return p, keyError(receiptCheckKind, key, "the payload does not decode")
	}
	for _, name := range receiptCountKeys {
		if _, present := raw.Counts[name]; !present {
			return p, keyError(receiptCheckKind, key, "a count of the receipt is missing")
		}
	}
	return p, nil
}

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

// publish delivers one github_receipt_check message: it drops a message whose generation is no longer the pull request's current one, skips one the App already posted
// (its external id is the idempotency key, so at-least-once delivery posts once) and otherwise posts the run.
func (p *receiptCheckPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	repositoryID, pull, generation, ok := splitGenerationKey(receiptCheckKey, key)
	if !ok {
		return keyError(receiptCheckKind, key, "not a github_receipt_check key")
	}
	m, err := decodeReceiptCheck(key, payload)
	if err != nil {
		return err
	}
	if _, err := m.check(key); err != nil {
		return err
	}
	if m.RepositoryID != repositoryID || m.Pull != pull || m.Generation != generation {
		return keyError(receiptCheckKind, key, "the payload does not match its key")
	}
	record, found, err := p.ledger.Current(ctx, repositoryID, pull)
	switch {
	case err != nil:
		return fmt.Errorf("%s %s: read the ledger of the Receipt check: %w", receiptCheckKind, key, err)
	case !found:
		return keyError(receiptCheckKind, key, "the ledger of the Receipt check has no record of the pull request")
	case record.Generation != generation: // a newer state or head superseded this message; it is not rendered, as its reason may no longer exist
		return nil
	}
	title, summary, err := m.output(key)
	if err != nil {
		return err
	}
	run := github.CheckRun{Name: receiptCheckName, HeadSHA: m.HeadSHA, Status: "completed", Conclusion: m.State, Title: title, Summary: summary}
	if m.State == string(execution.CheckInProgress) {
		run.Status, run.Conclusion = "in_progress", ""
	}
	return postCheckRun(ctx, checkClient{runs: p.runs, instance: p.instance, appID: p.appID, logger: p.logger}, receiptCheckKind, key, m.Repo, run)
}
