//go:build integration

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// checkGitHub is the check runs of one commit: it lists what was posted, as GitHub does.
type checkGitHub struct {
	mu   sync.Mutex
	runs []map[string]any
}

func (g *checkGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/"+cmtRepo+"/commits/"+chkHead+"/check-runs":
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(g.runs), "check_runs": g.runs})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+cmtRepo+"/check-runs":
		var run map[string]any
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run["id"] = 9000 + len(g.runs)
		g.runs = append(g.runs, run)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": run["id"]})
	default:
		http.NotFound(w, r)
	}
}

func (g *checkGitHub) posted() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.runs...)
}

// TestCheckPublisherOnPostgreSQLPostsTheCurrentGenerationOnce publishes through the real check ledger, delivers
// with the relay to a GitHub that lists what was posted, and checks that a later pass posts nothing and that a
// superseded generation is dropped.
func TestCheckPublisherOnPostgreSQLPostsTheCurrentGenerationOnce(t *testing.T) {
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

	gh := &checkGitHub{runs: []map[string]any{}} // GitHub lists [], never null
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := cmtMust(rest.NewBudget(0.5, time.Now))
	client := cmtMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	publisher := &checkPublisher{runs: cmtMust(github.NewChecks(client)), ledger: store.Checks(), instance: newLazy(store.Instance), appID: chkAppID,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), reports: &fakeReports{err: governance.ErrNotFound}}
	relay := cmtMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCheckKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	ledger := store.Checks()
	publish := func(state checks.State, reason, reference string, minute int) checks.Record {
		t.Helper()
		result := checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: state, Reason: reason, Reference: reference}
		record, written, err := ledger.Publish(t.Context(), result, bootTime.Add(time.Duration(minute)*time.Minute), func(generation int64) (governance.OutboxMessage, error) {
			return newCheckMessage(cmtRepo, result, owner.TierIsolated, "V1", generation)
		})
		if err != nil || !written {
			t.Fatalf("publish %s: written %v, %v", reason, written, err)
		}
		return record
	}
	pass := func() {
		t.Helper()
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if record := publish(checks.InProgress, "approval-missing", "P1-R2", 1); record.Generation != 1 {
		t.Fatalf("generation %d, want 1", record.Generation)
	}
	pass()
	pass() // nothing is pending: the second pass posts nothing
	posted := gh.posted()
	if len(posted) != 1 || posted[0]["status"] != "in_progress" || posted[0]["head_sha"] != chkHead || posted[0]["name"] != "SuiteWard / Contract" {
		t.Fatalf("after the first generation: %v; outbox: %s", posted,
			scalar(t, databaseURL, "SELECT coalesce(string_agg(key || ' ' || state || ' ' || coalesce(last_error, ''), '; '), 'empty') FROM outbox"))
	}
	instance := cmtMust(store.Instance(t.Context()))
	if posted[0]["external_id"] != "suiteward:"+instance+":check:1001:7:1" {
		t.Fatalf("external id %v", posted[0]["external_id"])
	}

	publish(checks.Success, "ready", "P1-R2", 2) // generation 2, superseded before it is delivered
	if record := publish(checks.Failure, "integrity-failed", "P1-R2", 3); record.Generation != 3 {
		t.Fatalf("generation %d, want 3", record.Generation)
	}
	pass()
	pass()
	posted = gh.posted()
	if len(posted) != 2 || posted[1]["status"] != "completed" || posted[1]["conclusion"] != "failure" || posted[1]["external_id"] != "suiteward:"+instance+":check:1001:7:3" {
		t.Fatalf("after the superseded generation: %v", posted)
	}
	if summary, _ := posted[1]["output"].(map[string]any)["summary"].(string); !strings.Contains(summary, "failed its integrity check") {
		t.Fatalf("summary %q", summary)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM outbox WHERE kind = 'github_check' AND state = 'delivered'"); got != "3" {
		t.Fatalf("%s delivered messages, want 3 (the superseded one is delivered, not posted)", got)
	}
}
