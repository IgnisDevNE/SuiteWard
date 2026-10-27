//go:build integration

package app

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// mergeGitHub is the GitHub of the merged pull requests of bootRepo: the pull request reads, the closed listing, the
// trees and blobs of the merge commits, and the comments of each pull request.
type mergeGitHub struct {
	mu       sync.Mutex
	pulls    map[int]map[string]any
	trees    map[string]map[string]string // by merge commit: path to content
	blobs    map[string]string            // by blob sha
	comments map[int][]string
}

func newMergeGitHub() *mergeGitHub {
	return &mergeGitHub{pulls: map[int]map[string]any{}, trees: map[string]map[string]string{}, blobs: map[string]string{}, comments: map[int][]string{}}
}

func blobSHA(content string) string { return fmt.Sprintf("%x", sha1.Sum([]byte(content))) }

// merge makes GitHub report pull request number as merged at mergedAt into the commit sha, whose tree holds files.
func (g *mergeGitHub) merge(number int, head, sha string, files map[string]string, mergedAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pulls[number] = map[string]any{"number": number, "state": "closed", "merged": true, "merged_at": mergedAt.Format(time.RFC3339), "merge_commit_sha": sha,
		"updated_at": mergedAt.Format(time.RFC3339), "head": map[string]any{"ref": "feature", "sha": head, "repo": map[string]any{"full_name": bootRepo}},
		"base": map[string]any{"ref": "main", "sha": bootHead}}
	g.trees[sha] = files
	for _, content := range files {
		g.blobs[blobSHA(content)] = content
	}
}

func (g *mergeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := "/repos/" + bootRepo
	path, found := strings.CutPrefix(r.URL.Path, prefix)
	if !found {
		http.NotFound(w, r)
		return
	}
	reply := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch {
	case r.Method == http.MethodGet && path == "/pulls": // the closed listing: nothing besides what the open listing showed
		reply([]any{})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/pulls/"):
		number, _ := strconv.Atoi(strings.TrimPrefix(path, "/pulls/"))
		if pull, known := g.pulls[number]; known {
			reply(pull)
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/git/trees/"):
		files, known := g.trees[strings.TrimPrefix(path, "/git/trees/")]
		if !known {
			http.NotFound(w, r)
			return
		}
		entries := []map[string]any{}
		for name, content := range files {
			entries = append(entries, map[string]any{"path": name, "mode": "100644", "type": "blob", "sha": blobSHA(content)})
		}
		reply(map[string]any{"tree": entries, "truncated": false})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/git/blobs/"):
		content, known := g.blobs[strings.TrimPrefix(path, "/git/blobs/")]
		if !known {
			http.NotFound(w, r)
			return
		}
		reply(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
	case strings.HasPrefix(path, "/issues/") && strings.HasSuffix(path, "/comments"):
		number, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/issues/"), "/comments"))
		if r.Method == http.MethodPost {
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			g.comments[number] = append(g.comments[number], request.Body)
			w.WriteHeader(http.StatusCreated)
			reply(map[string]any{"id": 7000 + len(g.comments[number])})
			return
		}
		listed := make([]map[string]any, len(g.comments[number]))
		for i, body := range g.comments[number] {
			listed[i] = map[string]any{"id": 6000 + i, "body": body, "user": map[string]any{"login": "suiteward[bot]", "id": cmtBotID},
				"created_at": bootTime.Format(time.RFC3339), "updated_at": bootTime.Format(time.RFC3339)}
		}
		reply(listed)
	default:
		http.NotFound(w, r)
	}
}

func (g *mergeGitHub) posted(number int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.comments[number]...)
}

// requireMergeComment requires pull to hold the revision comment of its change proposal and the outcome comment of its merge, which ends with the marker of key
// and says every phrase of want.
func requireMergeComment(t *testing.T, gh *mergeGitHub, instance string, pull int, key string, want ...string) {
	t.Helper()
	posted := gh.posted(pull)
	if len(posted) != 2 { // the revision comment of its change proposal, and the outcome of its merge
		t.Fatalf("pull %d: %d comments, want the revision comment and one outcome comment", pull, len(posted))
	}
	var outcome string
	for _, body := range posted {
		if commandtext.HasMarker(body, instance, key) {
			outcome = body
		}
	}
	if outcome == "" {
		t.Fatalf("pull %d: no comment ends with the marker of %s: %v", pull, key, posted)
	}
	for _, phrase := range want {
		if !strings.Contains(outcome, phrase) {
			t.Fatalf("pull %d: the comment lacks %q:\n%s", pull, phrase, outcome)
		}
	}
}

// TestMergesJobOnPostgreSQLPromotesAndCommentsThroughTheRelay runs the whole of M1.7 against the real stores and a GitHub
// served over HTTP: the baseline is bootstrapped, two pull requests change tests/ and are approved through
// ProcessConsent, GitHub reports both merged, the merges job decides them in merge order, and the relay and the
// publisher post one comment on each. The first merge promotes V2; the second, approved against V1, is blocked. A
// replay of the job and of the relay posts nothing new.
func TestMergesJobOnPostgreSQLPromotesAndCommentsThroughTheRelay(t *testing.T) {
	databaseURL := newDatabase(t)
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
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
		c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
		for number, head := range heads {
			c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime})
			c.Comments[number] = nil
		}
		if _, err := store.RecordCycle(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	cycle(bootTime, map[int]string{4: bootHead})
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[cjHead2] = &bootTree{files: bootFiles("v2")}
	repository.trees[cjHead3] = &bootTree{files: bootFiles("v3")}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, problems: &bootstrapProblems{},
		logger: logger, now: func() time.Time { return bootTime.Add(time.Hour) }}
	consent := func(comment int64, proposal, pull int) {
		t.Helper()
		command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("consent:github-comment:" + strconv.FormatInt(comment, 10)),
			SourceCommandID: contract.SourceCommandID("github-comment:" + strconv.FormatInt(comment, 10)), Actor: bootOwnerPrincipal(), Action: contract.ApproveConsent, Order: testOrder(comment),
			Carrier: carrierID(bootRepoID, pull), Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(proposal), RevisionID: "R1"}}))
		if response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil || !response.Receipt.CurrentApprovalEligible {
			t.Fatalf("approve P%d-R1: eligible %v, error %v", proposal, response.Receipt.CurrentApprovalEligible, err)
		}
	}
	if _, err := bootstrap.run(t.Context()); err != nil { // the Suite
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, bootFiles("v1")) // P1-R1, as the commands job would
	consent(101, 1, 4)
	for range 3 { // the integrity assessment and the first canonical
		if _, err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	canonical := func() string {
		return scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID)))
	}
	if got := canonical(); got != "V1" {
		t.Fatalf("canonical %s, want V1", got)
	}

	// Two pull requests change the protected tests; the checks job proposes each change and the owner approves both against V1.
	clock := bootTime.Add(2 * time.Hour)
	cycle(clock, map[int]string{4: bootHead, 5: cjHead2, 6: cjHead3})
	checks := (&checksJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), integrity: store.Integrity(), commands: store,
		pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}).withExecution(store.Execution(), store.ReceiptChecks(), time.Now())
	if err := checks.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	consent(201, 2, 5)
	consent(202, 3, 6)

	// GitHub reports both merged, pull request 6 first, each into a merge commit whose tree is the approved head.
	gh := newMergeGitHub()
	gh.merge(6, cjHead3, mgSHA6, bootFiles("v3"), clock.Add(time.Minute))
	gh.merge(5, cjHead2, mgSHA5, bootFiles("v2"), clock.Add(2*time.Minute))
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	clock = clock.Add(5 * time.Minute)
	cycle(clock, map[int]string{4: bootHead}) // both pull requests left the open list
	clock = clock.Add(time.Minute)
	merges := &mergesJob{repos: []string{bootRepo}, reader: bootMust(github.NewReader(client)), owners: store, suites: store, content: artifacts, store: store, integrity: store.Integrity(),
		tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	if _, err := merges.run(t.Context()); err != nil {
		t.Fatalf("merges run: %v", err)
	}

	if got := canonical(); got != "V2" {
		t.Fatalf("canonical %s after the merges, want V2", got)
	}
	outcomes := scalar(t, databaseURL, "SELECT string_agg(carrier || ' ' || kind || ' ' || reason || ' ' || version_id, '; ' ORDER BY carrier) FROM pr_merges")
	if want := "github-pr:1001/5 blocked canonical-changed V2; github-pr:1001/6 promoted  V2"; outcomes != want {
		t.Fatalf("outcomes %q, want %q", outcomes, want)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM github_pulls WHERE merged AND merge_processed"); got != "2" {
		t.Fatalf("%s merged pull requests processed, want 2", got)
	}

	publisher := &commentPublisher{writer: bootMust(github.NewWriter(client)), bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil }), instance: newLazy(store.Instance),
		owners: store, suites: store, reports: newChangeReports(store, artifacts)}
	relay := bootMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCommentKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute, Logger: logger}))
	pass := func() {
		t.Helper()
		if err := relay.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	instance := bootMust(store.Instance(t.Context()))
	for range 2 {
		pass()
	}
	check := func(pull int, key string, want ...string) { requireMergeComment(t, gh, instance, pull, key, want...) }
	check(6, "merge:1001:6", "V2", mgSHA6, "did not run the tests")
	check(5, "merge:1001:5", "stays at V2", "canonical contract changed after")

	// A replay: the job decides nothing again and nothing is posted again.
	if _, err := merges.run(t.Context()); err != nil {
		t.Fatalf("merges replay: %v", err)
	}
	for range 2 {
		pass()
	}
	check(6, "merge:1001:6")
	check(5, "merge:1001:5")
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM outbox WHERE key LIKE 'merge:%' AND state = 'delivered'"); got != "2" {
		t.Fatalf("%s delivered merge messages, want 2", got)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM pr_merges"); got != "2" {
		t.Fatalf("%s merge outcomes after the replay, want 2", got)
	}
}
