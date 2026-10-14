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
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// commentGitHub is the comments endpoint of one pull request: it lists what was posted, all by the bot.
type commentGitHub struct {
	mu     sync.Mutex
	bodies []string
}

func (g *commentGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path != "/repos/"+bootRepo+"/issues/4/comments" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		comments := make([]map[string]any, len(g.bodies))
		for i, body := range g.bodies {
			comments[i] = map[string]any{"id": 5000 + i, "body": body, "user": map[string]any{"login": "suiteward[bot]", "id": cmtBotID},
				"created_at": bootTime.Format(time.RFC3339), "updated_at": bootTime.Format(time.RFC3339)}
		}
		_ = json.NewEncoder(w).Encode(comments)
	case http.MethodPost:
		var request struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.bodies = append(g.bodies, request.Body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 5000 + len(g.bodies) - 1})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (g *commentGitHub) posted() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.bodies...)
}

// TestCommentPublisherOnPostgreSQLPostsEachMessageOnce writes the bootstrap summary and a processed acknowledgment
// through the real store, delivers them with the relay to a GitHub that lists what was posted, and checks that a
// second relay pass and a redelivery post nothing.
func TestCommentPublisherOnPostgreSQLPostsEachMessageOnce(t *testing.T) {
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
	cycle := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: bootTime, Comments: map[int][]githubsync.ObservedComment{4: nil},
		OpenPulls: []githubsync.ObservedPull{{Number: 4, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}}}
	if err := store.RecordCycle(t.Context(), cycle); err != nil {
		t.Fatal(err)
	}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: newBootRepository(bootRepo, bootRepoID)}, calls: map[string]int{}}
	job := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, content: artifacts, pulls: store, tier: owner.TierIsolated,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return bootTime.Add(time.Hour) }}
	if err := job.run(t.Context()); err != nil { // the Suite, the proposal P1-R1 and its summary message
		t.Fatal(err)
	}
	ack := cmtMust(newProcessedAck(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: 4}, 101, "owner", owner.ProjectID(bootRepoID), "default", "consent:github-comment:101"))
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: "consent:github-comment:101", SourceCommandID: "github-comment:101", Actor: bootOwnerPrincipal(),
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}, Carrier: "github-pr:1001/4",
		Action: contract.ApproveConsent, Order: 101}))
	response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command, Outbox: []governance.OutboxMessage{ack}})
	if err != nil || !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("approve: eligible %v, %v", response.Receipt.CurrentApprovalEligible, err)
	}

	gh := &commentGitHub{}
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := cmtMust(rest.NewBudget(0.5, time.Now))
	client := cmtMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	publisher := &commentPublisher{writer: cmtMust(github.NewWriter(client)), bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil }),
		instance: newLazy(store.Instance), owners: store, suites: store}
	relay := cmtMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCommentKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	for range 2 {
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	instance := cmtMust(store.Instance(t.Context()))
	posted := gh.posted()
	if len(posted) != 2 {
		t.Fatalf("posted %d comments, want the summary and the acknowledgment", len(posted))
	}
	want := map[string]string{"bootstrap-summary:1001:P1-R1": "SuiteWard bootstrap proposal P1-R1", "ack:1001:101": "Approval recorded for P1-R1 by @owner"}
	for key, text := range want {
		found := false
		for _, body := range posted {
			if commandtext.HasMarker(body, instance, key) {
				found = strings.Contains(body, text)
			}
		}
		if !found {
			t.Fatalf("no posted comment states %q and ends with the marker of %s:\n%s", text, key, strings.Join(posted, "\n----\n"))
		}
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM outbox WHERE kind = 'github_comment' AND state = 'delivered'"); got != "2" {
		t.Fatalf("%s delivered messages, want 2", got)
	}
	for key := range want { // a redelivery: the readback finds the marker
		payload := scalar(t, databaseURL, "SELECT payload::text FROM outbox WHERE key = $1", key)
		if err := publisher.publish(t.Context(), key, json.RawMessage(payload)); err != nil {
			t.Fatalf("redeliver %s: %v", key, err)
		}
	}
	if got := len(gh.posted()); got != 2 {
		t.Fatalf("%d comments after the redelivery, want 2", got)
	}
}
