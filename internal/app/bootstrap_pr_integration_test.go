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

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
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

// prFlow is the world of the end-to-end test of M1.7b: the real stores, a GitHub served over HTTP and the jobs that read them.
type prFlow struct {
	t           *testing.T
	databaseURL string
	store       *postgres.Store
	artifacts   *filesystem.Store
	gh          *prGitHub
	reader      *github.Reader
	logger      *slog.Logger
	clock       time.Time
	checks      *checksJob
	relay       *river.Relay
	instance    string
}

func newPRFlow(t *testing.T) *prFlow {
	t.Helper()
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

	// GitHub: the default branch has no protected files; pull request 5 adds tests/.
	gh := &prGitHub{mergeGitHub: newMergeGitHub(), head: bootHead}
	gh.tree(bootHead, map[string]string{"src/main.go": "package main\n"})
	gh.tree(cjHead2, bootFiles("v1"))
	gh.tree(cjHead4, bootFiles("v1")) // the head of pull request 7, read only once V1 exists
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	f := &prFlow{t: t, databaseURL: databaseURL, store: store, artifacts: artifacts, gh: gh, reader: bootMust(github.NewReader(client)),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), clock: bootTime.Add(2 * time.Hour)}
	f.checks = &checksJob{repos: []string{bootRepo}, reader: f.reader, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), integrity: store.Integrity(),
		commands: store, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: f.logger, now: func() time.Time { return f.clock }}
	publisher := &commentPublisher{writer: bootMust(github.NewWriter(client)), bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil }), instance: newLazy(store.Instance),
		owners: store, suites: store}
	f.relay = bootMust(river.NewRelay(river.RelayConfig{Store: store, Publishers: map[string]river.Publisher{githubCommentKind: publisher.publish}, MaxAttempts: 3, Lease: time.Minute, Logger: f.logger}))
	f.instance = bootMust(store.Instance(t.Context()))
	return f
}

// cycle records a cycle at which exactly the pull requests of heads are open.
func (f *prFlow) cycle(at time.Time, heads map[int]string) {
	f.t.Helper()
	c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
	for number, head := range heads {
		c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime, HeadRepo: bootRepo})
		c.Comments[number] = nil
	}
	if _, err := f.store.RecordCycle(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *prFlow) consent(comment int64, proposal, pull int) {
	f.t.Helper()
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("consent:github-comment:" + strconv.FormatInt(comment, 10)),
		SourceCommandID: contract.SourceCommandID("github-comment:" + strconv.FormatInt(comment, 10)), Actor: bootOwnerPrincipal(), Action: contract.ApproveConsent, Order: testOrder(comment),
		Carrier: carrierID(bootRepoID, pull), Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(proposal), RevisionID: "R1"}}))
	if response, err := governance.ProcessConsent(f.t.Context(), f.store, governance.ConsentRequest{Command: command}); err != nil || !response.Receipt.CurrentApprovalEligible {
		f.t.Fatalf("approve P%d-R1: eligible %v, error %v", proposal, response.Receipt.CurrentApprovalEligible, err)
	}
}

func (f *prFlow) current(pull int) checks.Record {
	f.t.Helper()
	record, found, err := f.store.Checks().Current(f.t.Context(), bootRepoID, pull)
	if err != nil || !found {
		f.t.Fatalf("the check of pull %d: found %v, error %v", pull, found, err)
	}
	return record
}

func (f *prFlow) step() {
	f.t.Helper()
	f.clock = f.clock.Add(time.Minute)
	if err := f.checks.run(f.t.Context()); err != nil {
		f.t.Fatalf("checks run: %v", err)
	}
}

func (f *prFlow) pass() {
	f.t.Helper()
	for range 2 {
		if err := f.relay.RunOnce(f.t.Context()); err != nil {
			f.t.Fatal(err)
		}
	}
}

// wantComment requires a comment on pull that ends with the marker of key and says every phrase.
func (f *prFlow) wantComment(pull int, key string, phrases ...string) {
	f.t.Helper()
	var found string
	for _, body := range f.gh.posted(pull) {
		if commandtext.HasMarker(body, f.instance, key) {
			found = body
		}
	}
	if found == "" {
		f.t.Fatalf("pull %d: no comment with the marker of %s among %d", pull, key, len(f.gh.posted(pull)))
	}
	for _, phrase := range phrases {
		if !strings.Contains(found, phrase) {
			f.t.Fatalf("pull %d: the comment of %s lacks %q:\n%s", pull, key, phrase, found)
		}
	}
}

// wantCheck requires the current check of pull to be state and reason under reference.
func (f *prFlow) wantCheck(pull int, state checks.State, reason, reference, what string) {
	f.t.Helper()
	if record := f.current(pull); record.State != state || record.Reason != reason || record.Reference != reference {
		f.t.Fatalf("check of pull %d %+v, want %s %s under %q%s", pull, record.Result, state, reason, reference, what)
	}
}

// wantComments requires pull to hold n comments.
func (f *prFlow) wantComments(pull, n int, what string) {
	f.t.Helper()
	if got := len(f.gh.posted(pull)); got != n {
		f.t.Fatalf("%d comments on pull %d%s, want %d", got, pull, what, n)
	}
}

// wantCanonical requires the Suite's current version, "none" without one.
func (f *prFlow) wantCanonical(version, when string) {
	f.t.Helper()
	if got := scalar(f.t, f.databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != version {
		f.t.Fatalf("canonical %s %s, want %s", got, when, version)
	}
}

// bootstrapWaits runs the bootstrap job, which creates the Suite and then waits for an initial contract: it proposes nothing.
func (f *prFlow) bootstrapWaits() {
	f.t.Helper()
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[bootHead] = &bootTree{files: map[string]string{"src/main.go": "package main\n"}}
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}, owners: f.store, suites: f.store,
		problems: &bootstrapProblems{}, logger: f.logger, now: func() time.Time { return f.clock }}
	for range 2 {
		if _, err := bootstrap.run(f.t.Context()); err != nil {
			f.t.Fatal(err)
		}
	}
	if got := scalar(f.t, f.databaseURL, "SELECT count(*)::text FROM proposals WHERE project_id = $1", string(owner.ProjectID(bootRepoID))); got != "0" {
		f.t.Fatalf("%s proposals after the bootstrap job, want none: the default branch has no protected files", got)
	}
}

// proposeOnPull5 proposes the bootstrap on pull request 5 as the commands job does: P1-R1 from its head, with its summary.
func (f *prFlow) proposeOnPull5() {
	f.t.Helper()
	tree := &bootTree{files: bootFiles("v1")}
	summary := bootMust(newSummaryMessage(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: 5},
		inventoryInputs(bootstrapSummaryInputs{Proposal: 1, Revision: 1, Source: sourcePull, Origin: cjHead2, Tier: "isolated"}, bootMust(inventory.Build(f.t.Context(), tree)))))
	if _, err := governance.ProposeBaseline(f.t.Context(), f.store, f.artifacts, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: defaultSuite, ProposalID: "P1", RevisionID: "R1"},
		Carrier:   carrierID(bootRepoID, 5), Origin: cjHead2, OriginKind: contract.OriginPull, Tree: tree, Outbox: []governance.OutboxMessage{summary}}); err != nil {
		f.t.Fatalf("propose the bootstrap: %v", err)
	}
}

// carrierSummary is steps 1 and 2: the pull request that carries the proposal has its summary comment and approval-missing; the other one has its notice.
func (f *prFlow) carrierSummary() {
	f.t.Helper()
	f.step()
	f.pass()
	f.wantCheck(5, checks.InProgress, "approval-missing", "P1-R1", "")
	f.wantComment(5, "bootstrap-summary:1001:P1-R1", "exact head of this pull request", "lets this pull request merge", cjHead2)
	f.wantComments(5, 1, "")
	f.wantCheck(7, checks.InProgress, "bootstrap-awaiting", "", ": it carries no proposal")
	f.wantComment(7, "bootstrap-notice:1001:7", "/suiteward propose", "/suiteward skip <code>")
	f.wantComments(7, 1, "")
}

// approved is steps 3 and 4: the owner approves the exact revision and the check says bootstrap-ready.
func (f *prFlow) approved() {
	f.t.Helper()
	f.consent(201, 1, 5)
	f.step()
	f.wantCheck(5, checks.Success, "bootstrap-ready", "P1-R1", "")
	f.pass()
	if len(f.gh.posted(7)) != 1 || len(f.gh.posted(5)) != 1 {
		f.t.Fatalf("%d comments on pull 7 and %d on pull 5 after more runs, want one each: the notice and the summary are written once", len(f.gh.posted(7)), len(f.gh.posted(5)))
	}
	f.wantCanonical("none", "before the merge")
}

// merged is steps 5 and 6: GitHub reports the merge, whose tree is the approved head; the merges job promotes V1 and says so once.
func (f *prFlow) merged() {
	f.t.Helper()
	t := f.t
	f.gh.merge(5, cjHead2, mgSHA5, bootFiles("v1"), f.clock.Add(time.Minute))
	f.clock = f.clock.Add(5 * time.Minute)
	f.cycle(f.clock, map[int]string{7: cjHead4}) // pull request 5 merged; pull request 7 stays open
	f.clock = f.clock.Add(time.Minute)
	merges := &mergesJob{repos: []string{bootRepo}, reader: f.reader, owners: f.store, suites: f.store, content: f.artifacts, store: f.store, integrity: f.store.Integrity(),
		tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: f.logger, now: func() time.Time { return f.clock }}
	if _, err := merges.run(t.Context()); err != nil {
		t.Fatalf("merges run: %v", err)
	}
	f.wantCanonical("V1", "after the merge")
	if got := scalar(t, f.databaseURL, "SELECT string_agg(carrier || ' ' || kind || ' ' || reason || ' ' || coalesce(version_id, ''), '; ' ORDER BY carrier) FROM pr_merges"); got != "github-pr:1001/5 promoted  V1" {
		t.Fatalf("merge outcomes %q, want the promotion of V1", got)
	}
	f.pass()
	f.wantComment(5, "merge:1001:5", "V1", "first canonical version", mgSHA5, "did not run the tests")
	f.wantComments(5, 2, "")
	if _, err := merges.run(t.Context()); err != nil { // a replay decides and posts nothing again
		t.Fatalf("merges replay: %v", err)
	}
	f.pass()
	f.wantComments(5, 2, " after a replay")
}

// secondPull is step 7: a second pull request changes tests/ and is proposed against V1, with a revision comment and approval-missing.
func (f *prFlow) secondPull() {
	f.t.Helper()
	f.gh.tree(cjHead3, bootFiles("v2"))
	f.cycle(f.clock, map[int]string{6: cjHead3, 7: cjHead4})
	f.step()
	f.pass()
	f.wantCheck(6, checks.InProgress, "approval-missing", "P2-R1", " against V1")
	f.wantComment(6, "revision:1001:P2-R1", "V1", "tests/a_test.go", "/suiteward approve P2-R1 <code>", "does not run tests")
	f.wantComments(6, 1, "")
	if record := f.current(7); record.State != checks.Success || record.Reason != "unchanged" || len(f.gh.posted(7)) != 1 {
		f.t.Fatalf("check of pull 7 %+v with %d comments, want success unchanged against V1 and still only the notice", record.Result, len(f.gh.posted(7)))
	}
	f.step()
	f.pass()
	f.wantComments(6, 1, " after another run")
}

// TestBootstrapFromAPullRequestOnPostgreSQLEndToEnd runs M1.7b against the real stores and a GitHub served over HTTP: a
// repository whose default branch has no protected files gets its first canonical from the merge of a pull request.
// The owner proposes the bootstrap on the pull request that adds tests/ (the commands job writes it), approves it through
// ProcessConsent, the check says bootstrap-ready, GitHub reports the merge, the merges job promotes V1, and the relay
// posts the summary and the outcome. A second pull request that changes tests/ is then proposed against V1 and gets a
// revision comment.
func TestBootstrapFromAPullRequestOnPostgreSQLEndToEnd(t *testing.T) {
	f := newPRFlow(t)
	f.cycle(f.clock, map[int]string{5: cjHead2, 7: cjHead4}) // pull request 7 carries nothing
	f.bootstrapWaits()
	f.proposeOnPull5()
	f.carrierSummary()
	f.approved()
	f.merged()
	f.secondPull()
}
