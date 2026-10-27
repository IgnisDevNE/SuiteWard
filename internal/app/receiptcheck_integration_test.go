//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// TestReceiptCheckPublisherOnPostgreSQLPostsTheCurrentGenerationOnce publishes through the real ledger of the Receipt check, delivers with the relay and the real
// check-run adapter to a GitHub that lists what was posted, and checks that a later pass posts nothing, that a superseded generation is dropped and that a receipt that
// was not run is posted neutral.
func TestReceiptCheckPublisherOnPostgreSQLPostsTheCurrentGenerationOnce(t *testing.T) {
	databaseURL := newDatabase(t)
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	pool, store, _, err := connectWithArtifacts(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	gh := &checkGitHub{runs: []map[string]any{}}
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := cmtMust(rest.NewBudget(0.5, time.Now))
	client := cmtMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledger := store.ReceiptChecks()
	publisher := &receiptCheckPublisher{runs: cmtMust(github.NewChecks(client)), ledger: ledger, instance: newLazy(store.Instance), appID: chkAppID, logger: logger}
	relay := cmtMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{receiptCheckKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute, Logger: logger}))
	publish := func(d receiptDecision, minute int) execution.ReceiptRecord {
		t.Helper()
		record, written, err := ledger.Publish(t.Context(), d.check, bootTime.Add(time.Duration(minute)*time.Minute), func(generation int64) (governance.OutboxMessage, error) {
			return newReceiptCheckMessage(cmtRepo, d, owner.TierIsolated, generation)
		})
		if err != nil || !written {
			t.Fatalf("publish %s: written %v, %v", d.check.Reason, written, err)
		}
		return record
	}
	pass := func() {
		t.Helper()
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if record := publish(rcpDecision(execution.CheckInProgress, receiptRunning), 1); record.Generation != 1 {
		t.Fatalf("generation %d, want 1", record.Generation)
	}
	pass()
	pass() // nothing is pending: the second pass posts nothing
	posted := gh.posted()
	if len(posted) != 1 || posted[0]["status"] != "in_progress" || posted[0]["head_sha"] != chkHead || posted[0]["name"] != "SuiteWard / Receipt" {
		t.Fatalf("after the first generation: %v; outbox: %s", posted,
			scalar(t, databaseURL, "SELECT coalesce(string_agg(key || ' ' || state || ' ' || coalesce(last_error, ''), '; '), 'empty') FROM outbox"))
	}
	instance := cmtMust(store.Instance(t.Context()))
	if posted[0]["external_id"] != "suiteward:"+instance+":receipt-check:1001:7:1" {
		t.Fatalf("external id %v", posted[0]["external_id"])
	}

	publish(rcpDecision(execution.CheckSuccess, receiptComplete), 2) // generation 2, superseded before it is delivered
	if record := publish(rcpDecision(execution.CheckNeutral, receiptNotRun), 3); record.Generation != 3 {
		t.Fatalf("generation %d, want 3", record.Generation)
	}
	pass()
	pass()
	posted = gh.posted()
	if len(posted) != 2 || posted[1]["status"] != "completed" || posted[1]["conclusion"] != "neutral" || posted[1]["external_id"] != "suiteward:"+instance+":receipt-check:1001:7:3" {
		t.Fatalf("after the superseded generation: %v", posted)
	}
	if summary, _ := posted[1]["output"].(map[string]any)["summary"].(string); !strings.Contains(summary, "no executor was seen in time") {
		t.Fatalf("summary %q", summary)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM outbox WHERE kind = 'github_receipt_check' AND state = 'delivered'"); got != "3" {
		t.Fatalf("%s delivered messages, want 3 (the superseded one is delivered, not posted)", got)
	}
}
