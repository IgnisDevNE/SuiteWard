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
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// prGitHub is mergeGitHub with the default branch: its head and the trees of the heads of the pull requests.
type prGitHub struct {
	*mergeGitHub
	head string // the head of the default branch
}

func (g *prGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/repos/"+bootRepo+"/branches/main" {
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"sha": g.head}})
		return
	}
	g.mergeGitHub.ServeHTTP(w, r)
}

// tree makes GitHub answer the tree of commit sha with files.
func (g *prGitHub) tree(sha string, files map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trees[sha] = files
	for _, content := range files {
		g.blobs[blobSHA(content)] = content
	}
}

// TestBootstrapFromAPullRequestOnPostgreSQLEndToEnd runs M1.7b against the real stores and a GitHub served over HTTP: a
// repository whose default branch has no protected files gets its first canonical from the merge of a pull request.
// The owner proposes the bootstrap on the pull request that adds tests/ (the commands job writes it), approves it through
// ProcessConsent, the check says bootstrap-ready, GitHub reports the merge, the merges job promotes V1, and the relay
// posts the summary and the outcome. A second pull request that changes tests/ is then proposed against V1 and gets a
// revision comment.
func TestBootstrapFromAPullRequestOnPostgreSQLEndToEnd(t *testing.T) {
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
			c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime, HeadRepo: bootRepo})
			c.Comments[number] = nil
		}
		if err := store.RecordCycle(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	consent := func(comment int64, proposal, pull int) {
		t.Helper()
		command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("consent:github-comment:" + strconv.FormatInt(comment, 10)),
			SourceCommandID: contract.SourceCommandID("github-comment:" + strconv.FormatInt(comment, 10)), Actor: bootOwnerPrincipal(), Action: contract.ApproveConsent, Order: contract.CommandOrder(comment),
			Carrier: carrierID(bootRepoID, pull), Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(proposal), RevisionID: "R1"}}))
		if response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil || !response.Receipt.CurrentApprovalEligible {
			t.Fatalf("approve P%d-R1: eligible %v, error %v", proposal, response.Receipt.CurrentApprovalEligible, err)
		}
	}
	current := func(pull int) checks.Record {
		t.Helper()
		record, found, err := store.Checks().Current(t.Context(), bootRepoID, pull)
		if err != nil || !found {
			t.Fatalf("the check of pull %d: found %v, error %v", pull, found, err)
		}
		return record
	}

	// GitHub: the default branch has no protected files; pull request 5 adds tests/.
	gh := &prGitHub{mergeGitHub: newMergeGitHub(), head: bootHead}
	gh.tree(bootHead, map[string]string{"src/main.go": "package main\n"})
	gh.tree(cjHead2, bootFiles("v1"))
	gh.tree(cjHead4, bootFiles("v1")) // the head of pull request 7, read only once V1 exists
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	reader := bootMust(github.NewReader(client))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := bootTime.Add(2 * time.Hour)
	cycle(clock, map[int]string{5: cjHead2, 7: cjHead4}) // pull request 7 carries nothing

	// The bootstrap job creates the Suite and then waits for an initial contract: it proposes nothing.
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[bootHead] = &bootTree{files: map[string]string{"src/main.go": "package main\n"}}
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}, owners: store, suites: store,
		problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	for range 2 {
		if err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM proposals WHERE project_id = $1", string(owner.ProjectID(bootRepoID))); got != "0" {
		t.Fatalf("%s proposals after the bootstrap job, want none: the default branch has no protected files", got)
	}

	// The owner proposes the bootstrap on pull request 5, as the commands job does: P1-R1 from its head, with its summary.
	tree := &bootTree{files: bootFiles("v1")}
	summary := bootMust(newSummaryMessage(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: 5},
		inventoryInputs(bootstrapSummaryInputs{Proposal: 1, Revision: 1, Source: sourcePull, Origin: cjHead2, Tier: "isolated"}, bootMust(inventory.Build(t.Context(), tree)))))
	if _, err := governance.ProposeBaseline(t.Context(), store, artifacts, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: defaultSuite, ProposalID: "P1", RevisionID: "R1"},
		Carrier:   carrierID(bootRepoID, 5), Origin: cjHead2, OriginKind: contract.OriginPull, Tree: tree, Outbox: []governance.OutboxMessage{summary}}); err != nil {
		t.Fatalf("propose the bootstrap: %v", err)
	}

	checksJob := &checksJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), integrity: store.Integrity(),
		commands: store, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	step := func() {
		t.Helper()
		clock = clock.Add(time.Minute)
		if err := checksJob.run(t.Context()); err != nil {
			t.Fatalf("checks run: %v", err)
		}
	}
	publisher := &commentPublisher{writer: bootMust(github.NewWriter(client)), bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil }), instance: newLazy(store.Instance),
		owners: store, suites: store}
	relay := bootMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCommentKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute, Logger: logger}))
	pass := func() {
		t.Helper()
		for range 2 {
			if err := relay.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
	}
	instance := bootMust(store.Instance(t.Context()))
	wantComment := func(pull int, key string, phrases ...string) {
		t.Helper()
		var found string
		for _, body := range gh.posted(pull) {
			if commandtext.HasMarker(body, instance, key) {
				found = body
			}
		}
		if found == "" {
			t.Fatalf("pull %d: no comment with the marker of %s among %d", pull, key, len(gh.posted(pull)))
		}
		for _, phrase := range phrases {
			if !strings.Contains(found, phrase) {
				t.Fatalf("pull %d: the comment of %s lacks %q:\n%s", pull, key, phrase, found)
			}
		}
	}

	// 1 and 2. The pull request that carries the proposal: its summary comment and approval-missing.
	step()
	pass()
	if record := current(5); record.State != checks.InProgress || record.Reason != "approval-missing" || record.Reference != "P1-R1" {
		t.Fatalf("check of pull 5 %+v, want in_progress approval-missing under P1-R1", record.Result)
	}
	wantComment(5, "bootstrap-summary:1001:P1-R1", "exact head of this pull request", "lets this pull request merge", cjHead2)
	if len(gh.posted(5)) != 1 {
		t.Fatalf("%d comments on pull 5, want only the summary", len(gh.posted(5)))
	}
	if record := current(7); record.State != checks.InProgress || record.Reason != "bootstrap-awaiting" || record.Reference != "" {
		t.Fatalf("check of pull 7 %+v, want in_progress bootstrap-awaiting: it carries no proposal", record.Result)
	}
	wantComment(7, "bootstrap-notice:1001:7", "/suiteward propose", "/suiteward skip <code>")
	if len(gh.posted(7)) != 1 {
		t.Fatalf("%d comments on pull 7, want only the notice", len(gh.posted(7)))
	}

	// 3 and 4. The owner approves the exact revision; the check says bootstrap-ready.
	consent(201, 1, 5)
	step()
	if record := current(5); record.State != checks.Success || record.Reason != "bootstrap-ready" || record.Reference != "P1-R1" {
		t.Fatalf("check of pull 5 %+v, want success bootstrap-ready under P1-R1", record.Result)
	}
	pass()
	if len(gh.posted(7)) != 1 || len(gh.posted(5)) != 1 {
		t.Fatalf("%d comments on pull 7 and %d on pull 5 after more runs, want one each: the notice and the summary are written once", len(gh.posted(7)), len(gh.posted(5)))
	}
	if got := scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != "none" {
		t.Fatalf("canonical %s before the merge, want none", got)
	}

	// 5 and 6. GitHub reports the merge, whose tree is the approved head; the merges job promotes V1 and says so once.
	gh.merge(5, cjHead2, mgSHA5, bootFiles("v1"), clock.Add(time.Minute))
	clock = clock.Add(5 * time.Minute)
	cycle(clock, map[int]string{7: cjHead4}) // pull request 5 merged; pull request 7 stays open
	clock = clock.Add(time.Minute)
	merges := &mergesJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, content: artifacts, store: store, integrity: store.Integrity(),
		tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	if err := merges.run(t.Context()); err != nil {
		t.Fatalf("merges run: %v", err)
	}
	if got := scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != "V1" {
		t.Fatalf("canonical %s after the merge, want V1", got)
	}
	if got := scalar(t, databaseURL, "SELECT string_agg(carrier || ' ' || kind || ' ' || reason || ' ' || coalesce(version_id, ''), '; ' ORDER BY carrier) FROM pr_merges"); got != "github-pr:1001/5 promoted  V1" {
		t.Fatalf("merge outcomes %q, want the promotion of V1", got)
	}
	pass()
	wantComment(5, "merge:1001:5", "V1", "first canonical version", mgSHA5, "did not run the tests")
	if len(gh.posted(5)) != 2 {
		t.Fatalf("%d comments on pull 5, want the summary and one merge comment", len(gh.posted(5)))
	}
	if err := merges.run(t.Context()); err != nil { // a replay decides and posts nothing again
		t.Fatalf("merges replay: %v", err)
	}
	pass()
	if len(gh.posted(5)) != 2 {
		t.Fatalf("%d comments on pull 5 after a replay, want 2", len(gh.posted(5)))
	}

	// 7. A second pull request changes tests/: proposed against V1, with a revision comment and approval-missing.
	gh.tree(cjHead3, bootFiles("v2"))
	cycle(clock, map[int]string{6: cjHead3, 7: cjHead4})
	step()
	pass()
	if record := current(6); record.State != checks.InProgress || record.Reason != "approval-missing" || record.Reference != "P2-R1" {
		t.Fatalf("check of pull 6 %+v, want in_progress approval-missing under P2-R1 against V1", record.Result)
	}
	wantComment(6, "revision:1001:P2-R1", "V1", "tests/a_test.go", "/suiteward approve P2-R1 <code>", "does not run tests")
	if len(gh.posted(6)) != 1 {
		t.Fatalf("%d comments on pull 6, want only the revision comment", len(gh.posted(6)))
	}
	if record := current(7); record.State != checks.Success || record.Reason != "unchanged" || len(gh.posted(7)) != 1 {
		t.Fatalf("check of pull 7 %+v with %d comments, want success unchanged against V1 and still only the notice", record.Result, len(gh.posted(7)))
	}
	step()
	pass()
	if len(gh.posted(6)) != 1 {
		t.Fatalf("%d comments on pull 6 after another run, want still one", len(gh.posted(6)))
	}
}
