//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// requireStoredCheck requires the stored check of pull to be state and reason under reference.
func requireStoredCheck(t *testing.T, store *postgres.Store, pull int, state checks.State, reason, reference string) {
	t.Helper()
	record, found, err := store.Checks().Current(t.Context(), bootRepoID, pull)
	if err != nil || !found {
		t.Fatalf("the check of pull %d: found %v, error %v", pull, found, err)
	}
	if record.State != state || record.Reason != reason || record.Reference != reference {
		t.Fatalf("check of pull %d is %s %s under %q, want %s %s under %q", pull, record.State, record.Reason, record.Reference, state, reason, reference)
	}
}

// requireCommandLedger requires the command ledger to hold the dispositions of want, "<comment id>=<disposition>" in the order of the comments.
func requireCommandLedger(t *testing.T, databaseURL, want string) {
	t.Helper()
	if got := scalar(t, databaseURL, "SELECT string_agg(comment_id || '=' || disposition, ' ' ORDER BY comment_id) FROM pr_commands"); got != want {
		t.Fatalf("ledger = %q; want %q", got, want)
	}
}

// requireLatestProposal requires the Suite's latest bootstrap proposal to be reference, carried by pull, with the origin kind and commit.
func requireLatestProposal(t *testing.T, store *postgres.Store, reference string, pull int, kind contract.OriginKind, origin string) {
	t.Helper()
	err := store.Do(t.Context(), owner.ProjectID(bootRepoID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil || !found {
			t.Fatalf("the latest bootstrap proposal: found %v, error %v", found, err)
		}
		proposal, _, err := tx.Proposal(ctx, id)
		if err != nil {
			return err
		}
		current := proposal.Current()
		if got := referenceText(current.Binding().Reference()); got != reference || current.Carrier() != carrierID(bootRepoID, pull) || current.OriginKind() != kind || string(current.Origin()) != origin {
			t.Fatalf("latest proposal %s carried by %s from %s %s, want %s carried by pull %d from %s %s", got, current.Carrier(), current.OriginKind(), current.Origin(), reference, pull, kind, origin)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBootstrapFlowAcrossTheJobsOnPostgreSQL runs the explicit bootstrap through the commands, checks and merges jobs
// against the real stores, as it happened on SuiteWardQ2. Each job's decision is read back from PostgreSQL by the next:
// propose on a pull request whose head holds the declaration, beside an older proposal, then a protected push that
// cancels it, a second propose with the next number, a skip on another pull request, an approval with a fresh code, and
// the merge that gives V1.
func TestBootstrapFlowAcrossTheJobsOnPostgreSQL(t *testing.T) {
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
	secret := make([]byte, totp.SecretBytes)
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: secret, Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The Suite exists, as the bootstrap job leaves it, with an older proposal P1 carried by pull request 4.
	if err := store.CreateSuite(t.Context(), owner.ProjectID(bootRepoID), defaultSuite, state.PolicyRevision, contract.IntegrationTargetID(targetPrefix+"main")); err != nil {
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, bootFiles("v1"))

	// GitHub: the default branch has no declaration; the heads of pull requests 5 and 6 are read for the commands and checks jobs.
	mainFiles := map[string]string{"src/main.go": "package main\n"}
	gh := &prGitHub{mergeGitHub: newMergeGitHub(), head: bootHead}
	gh.tree(bootHead, mainFiles)
	gh.tree(cjHead3, mainFiles)
	gh.tree(cjHead4, mainFiles)
	gh.tree(cjHead2, bootFiles("v1")) // pull request 5, as first pushed
	gh.tree(cjHead5, bootFiles("v2")) // and after the protected push
	server := httptest.NewServer(gh)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(ctx context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	reader := bootMust(github.NewReader(client))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	clock := bootTime.Add(time.Hour)
	heads := map[int]string{4: cjHead3, 5: cjHead2, 6: cjHead4}
	listed := map[int][]githubsync.ObservedComment{}
	// observe lists comment (when given) on pull and records a cycle ten minutes later, so the commands job may decide it.
	observe := func(pull int, comment *githubsync.ObservedComment) {
		t.Helper()
		if comment != nil {
			listed[pull] = append(listed[pull], *comment)
		}
		clock = clock.Add(10 * time.Minute)
		cycle := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: clock, StartedAt: clock, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
		for number, head := range heads {
			cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: clock, HeadRepo: bootRepo})
			cycle.Comments[number] = listed[number]
		}
		if _, err := store.RecordCycle(t.Context(), cycle); err != nil {
			t.Fatal(err)
		}
	}
	command := func(id int64, body func(code string) string) *githubsync.ObservedComment {
		code, err := totp.Code(secret, totp.Step(clock))
		if err != nil {
			t.Fatal(err)
		}
		return &githubsync.ObservedComment{CommentID: id, AuthorID: bootOwnerID, AuthorLogin: "owner", CreatedAt: clock, UpdatedAt: clock, Body: body(code)}
	}
	commandsJob := &commandsJob{ledger: store, owners: store, suites: store, syncs: store, repos: []string{bootRepo}, reader: reader, content: artifacts, pulls: store, tier: owner.TierIsolated,
		logger: logger, now: func() time.Time { return clock },
		bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil })}
	runCommands := func() {
		t.Helper()
		if _, err := commandsJob.run(t.Context()); err != nil {
			t.Fatalf("commands run: %v", err)
		}
	}
	checksJob := &checksJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), integrity: store.Integrity(),
		commands: store, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	runChecks := func() {
		t.Helper()
		clock = clock.Add(time.Minute)
		if err := checksJob.run(t.Context()); err != nil {
			t.Fatalf("checks run: %v", err)
		}
	}
	wantCheck := func(pull int, state checks.State, reason, reference string) {
		t.Helper()
		requireStoredCheck(t, store, pull, state, reason, reference)
	}
	wantLedger := func(want string) {
		t.Helper()
		requireCommandLedger(t, databaseURL, want)
	}
	// wantProposal requires the Suite's latest bootstrap proposal to be reference, carried by pull, with the origin kind and commit.
	wantProposal := func(reference string, pull int, kind contract.OriginKind, origin string) {
		t.Helper()
		requireLatestProposal(t, store, reference, pull, kind, origin)
	}

	// 1. Propose on pull request 5, whose head holds .suiteward.yml and whose default branch does not: P2-R1, beside the older P1.
	observe(5, command(301, func(string) string { return "/suiteward propose" }))
	runCommands()
	wantLedger("301=proposed")
	wantProposal("P2-R1", 5, contract.OriginPull, cjHead2)

	// 2. The checks job: the carrier waits for approval, every other pull request for a proposal.
	runChecks()
	wantCheck(5, checks.InProgress, "approval-missing", "P2-R1")
	wantCheck(4, checks.InProgress, "bootstrap-awaiting", "")
	wantCheck(6, checks.InProgress, "bootstrap-awaiting", "")

	// 3. A protected push to the carrier cancels the proposal: one cancelled comment, however many runs follow.
	heads[5] = cjHead5
	observe(5, nil)
	runChecks()
	wantCheck(5, checks.InProgress, "bootstrap-awaiting", "P2-R1")
	runChecks()
	if got := scalar(t, databaseURL, "SELECT string_agg(key, ' ' ORDER BY key) FROM outbox WHERE key LIKE 'bootstrap-cancelled:%'"); got != "bootstrap-cancelled:1001:P2-R1" {
		t.Fatalf("cancelled comments %q, want one for P2-R1", got)
	}

	// 4. Propose again: the next number, from the new head.
	observe(5, command(302, func(string) string { return "/suiteward propose" }))
	runCommands()
	wantLedger("301=proposed 302=proposed")
	wantProposal("P3-R1", 5, contract.OriginPull, cjHead5)
	runChecks()
	wantCheck(5, checks.InProgress, "approval-missing", "P3-R1")

	// 5. A skip on another pull request is read back from PostgreSQL by the checks job.
	observe(6, command(303, func(code string) string { return "/suiteward skip " + code }))
	runCommands()
	wantLedger("301=proposed 302=proposed 303=skipped")
	if skipped, err := store.Skipped(t.Context(), bootRepoID, 6, bootTime); err != nil || !skipped {
		t.Fatalf("Skipped(pull 6) = %v, %v; want true", skipped, err)
	}
	runChecks()
	wantCheck(6, checks.Success, "bootstrap-skipped", "")
	wantCheck(4, checks.InProgress, "bootstrap-awaiting", "")

	// 6. Approve the exact revision with a fresh code: the check is bootstrap-ready.
	observe(5, command(304, func(code string) string { return "/suiteward approve P3-R1 " + code }))
	runCommands()
	wantLedger("301=proposed 302=proposed 303=skipped 304=processed")
	runChecks()
	wantCheck(5, checks.Success, "bootstrap-ready", "P3-R1")
	if got := scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != "none" {
		t.Fatalf("canonical %s before the merge, want none", got)
	}

	// 7. GitHub reports the merge of pull request 5; the merges job gives V1 from the merge commit.
	gh.merge(5, cjHead5, mgSHA5, bootFiles("v2"), clock.Add(time.Minute))
	delete(heads, 5)
	clock = clock.Add(5 * time.Minute)
	observe(5, nil)
	clock = clock.Add(time.Minute)
	merges := &mergesJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, content: artifacts, store: store, integrity: store.Integrity(),
		tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}
	if _, err := merges.run(t.Context()); err != nil {
		t.Fatalf("merges run: %v", err)
	}
	if got := scalar(t, databaseURL, "SELECT coalesce(current_version_id, 'none') FROM suites WHERE project_id = $1 AND suite_id = 'default'", string(owner.ProjectID(bootRepoID))); got != "V1" {
		t.Fatalf("canonical %s after the merge, want V1", got)
	}
	if got := scalar(t, databaseURL, "SELECT string_agg(carrier || ' ' || kind || ' ' || reason || ' ' || coalesce(version_id, '') || ' ' || source, '; ' ORDER BY carrier) FROM pr_merges"); got != "github-pr:1001/5 promoted  V1 "+mgSHA5 {
		t.Fatalf("merge outcomes %q, want the promotion of V1 from the merge commit", got)
	}
}
