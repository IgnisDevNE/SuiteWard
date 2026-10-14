//go:build integration

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// headsGitHub is the check runs of every commit of bootRepo: it lists what was posted for a commit, as GitHub does.
type headsGitHub struct {
	mu   sync.Mutex
	runs []map[string]any
}

func (g *headsGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := "/repos/" + bootRepo + "/commits/"
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/check-runs"):
		sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/check-runs")
		listed := []map[string]any{}
		for _, run := range g.runs {
			if run["head_sha"] == sha {
				listed = append(listed, run)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(listed), "check_runs": listed})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/"+bootRepo+"/check-runs":
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

// posted lists "<head prefix> <status or conclusion> <generation>" of every run, in posting order.
func (g *headsGitHub) posted(t *testing.T, instance string) []string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	var posted []string
	for _, run := range g.runs {
		state := run["status"].(string)
		if state == "completed" {
			state = run["conclusion"].(string)
		}
		external, _ := run["external_id"].(string)
		posted = append(posted, run["head_sha"].(string)[:1]+" "+state+" "+strings.TrimPrefix(external, "suiteward:"+instance+":"))
	}
	return posted
}

// TestChecksJobOnPostgreSQLPublishesTheStatesThroughTheRelay runs the check job against the real stores and its messages
// through the relay and the publisher to a GitHub that lists what was posted: an unchanged pull request succeeds, a
// protected change waits for approval, an approval makes it succeed and a revocation makes it wait again, and a
// generation that a newer one superseded before the relay ran never posts.
func TestChecksJobOnPostgreSQLPublishesTheStatesThroughTheRelay(t *testing.T) {
	databaseURL := newDatabase(t)
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	pool, store, artifacts, err := connectWithArtifacts(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, 20), Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	cycle := func(at time.Time, heads map[int]string) {
		t.Helper()
		c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, Comments: map[int][]githubsync.ObservedComment{}}
		for number, head := range heads {
			c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime})
			c.Comments[number] = nil
		}
		if err := store.RecordCycle(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	cycle(bootTime, map[int]string{4: bootHead})
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[cjHead2] = &bootTree{files: bootFiles("v2")}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, content: artifacts, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{},
		logger: logger, now: func() time.Time { return bootTime.Add(time.Hour) }}
	consent := func(action contract.ConsentAction, comment int64, proposal, pull int) {
		t.Helper()
		command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("consent:github-comment:" + strconv.FormatInt(comment, 10)),
			SourceCommandID: contract.SourceCommandID("github-comment:" + strconv.FormatInt(comment, 10)), Actor: bootOwnerPrincipal(), Action: action, Order: contract.CommandOrder(comment),
			Carrier: carrierID(bootRepoID, pull), Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(proposal), RevisionID: "R1"}}))
		if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // the Suite and the proposal P1-R1
		if err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	consent(contract.ApproveConsent, 101, 1, 4)
	for range 3 { // the integrity assessment and the first canonical
		if err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != "V1" {
		t.Fatalf("canonical %s, want V1", got)
	}

	gh := &headsGitHub{}
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	publisher := &checkPublisher{runs: bootMust(github.NewChecks(client)), ledger: store.Checks(), instance: newLazy(store.Instance), appID: chkAppID, logger: logger}
	relay := bootMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCheckKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute, Logger: logger}))
	job := &checksJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), commands: store,
		pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: time.Now}
	instance := bootMust(store.Instance(t.Context()))
	step := func() {
		t.Helper()
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	pass := func() {
		t.Helper()
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	want := func(posted ...string) {
		t.Helper()
		if got := gh.posted(t, instance); strings.Join(got, "; ") != strings.Join(posted, "; ") {
			t.Fatalf("posted runs\n got %v\nwant %v\noutbox: %s", got, posted, scalar(t, databaseURL, "SELECT coalesce(string_agg(key || ' ' || state || ' ' || coalesce(last_error, ''), '; ' ORDER BY id), 'empty') FROM outbox WHERE kind = 'github_check'"))
		}
	}

	cycle(bootTime.Add(2*time.Hour), map[int]string{4: bootHead, 5: cjHead2})
	step()
	pass()
	want("1 success check:1001:4:1", "2 in_progress check:1001:5:1")
	step()
	pass() // nothing changed: nothing is posted again
	want("1 success check:1001:4:1", "2 in_progress check:1001:5:1")

	consent(contract.ApproveConsent, 201, 2, 5)
	step()
	pass()
	want("1 success check:1001:4:1", "2 in_progress check:1001:5:1", "2 success check:1001:5:2")

	consent(contract.RevokeConsent, 202, 2, 5)
	step()
	pass()
	want("1 success check:1001:4:1", "2 in_progress check:1001:5:1", "2 success check:1001:5:2", "2 in_progress check:1001:5:3")

	consent(contract.ApproveConsent, 203, 2, 5) // generation 4 is superseded by generation 5 before the relay runs
	step()
	consent(contract.RevokeConsent, 204, 2, 5)
	step()
	pass()
	pass()
	want("1 success check:1001:4:1", "2 in_progress check:1001:5:1", "2 success check:1001:5:2", "2 in_progress check:1001:5:3", "2 in_progress check:1001:5:5")
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM outbox WHERE kind = 'github_check' AND state = 'delivered'"); got != "6" {
		t.Fatalf("%s delivered check messages, want 6 (the superseded one is delivered, not posted)", got)
	}
}
