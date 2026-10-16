//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// TestCommandsJobOnPostgreSQLProposesAndSkips runs /suiteward propose and /suiteward skip against the real stores: the
// proposal and its bootstrap summary are written together, and a skip is a ledger entry the checks job can read.
func TestCommandsJobOnPostgreSQLProposesAndSkips(t *testing.T) {
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
	secret := make([]byte, totp.SecretBytes)
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: secret, Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	state, enrolled, err := store.Current(t.Context())
	if err != nil || !enrolled {
		t.Fatalf("enrollment: %v, %v", enrolled, err)
	}
	// The Suite is created here: the commands job proposes into an existing Suite, as the bootstrap job leaves it.
	if err := store.CreateSuite(t.Context(), owner.ProjectID(bootRepoID), defaultSuite, state.PolicyRevision, contract.IntegrationTargetID(targetPrefix+"main")); err != nil {
		t.Fatal(err)
	}
	pulls := []githubsync.ObservedPull{
		{Number: 4, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime},
		{Number: 5, HeadSHA: bootHead, HeadRef: "other", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime},
	}
	command := func(id int64, created time.Time, body func(code string) string) githubsync.ObservedComment {
		code, err := totp.Code(secret, totp.Step(created))
		if err != nil {
			t.Fatal(err)
		}
		return githubsync.ObservedComment{CommentID: id, AuthorID: bootOwnerID, AuthorLogin: "owner", CreatedAt: created, UpdatedAt: created, Body: body(code)}
	}
	comments := map[int][]githubsync.ObservedComment{
		4: {command(101, bootTime.Add(10*time.Minute), func(string) string { return "/suiteward propose" })},
		5: {command(102, bootTime.Add(20*time.Minute), func(code string) string { return "/suiteward skip " + code })},
	}
	if _, err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: bootTime.Add(time.Hour), StartedAt: bootTime.Add(time.Hour), Requests: 1, OpenPulls: pulls, Comments: comments}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: newBootRepository(bootRepo, bootRepoID)}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	job := &commandsJob{ledger: store, owners: store, suites: store, syncs: store, repos: []string{bootRepo}, reader: reader, content: artifacts, pulls: store, tier: owner.TierIsolated,
		logger: logger, now: func() time.Time { return bootTime.Add(2*time.Hour + 123456789*time.Nanosecond) },
		bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil })}
	for range 2 { // the second run finds nothing to decide
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := scalar(t, databaseURL, "SELECT string_agg(comment_id || '=' || disposition, ' ' ORDER BY comment_id) FROM pr_commands"), "101=proposed 102=skipped"; got != want {
		t.Fatalf("ledger = %q; want %q", got, want)
	}
	if got, want := scalar(t, databaseURL, "SELECT string_agg(key, ' ' ORDER BY key) FROM outbox WHERE key LIKE 'bootstrap-summary:%' OR key LIKE 'ack:%'"), "ack:1001:102 bootstrap-summary:1001:P1-R1"; got != want {
		t.Fatalf("outbox = %q; want %q (the proposal's summary is its acknowledgment, and a skip has its own)", got, want)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM totp_consumed_steps WHERE source_command_id LIKE 'github-comment:%'"); got != "1" {
		t.Fatalf("%s consumed steps; want 1 (the skip's, and none for the propose)", got)
	}
	skipped, err := store.Skipped(t.Context(), bootRepoID, 5, bootTime)
	if err != nil || !skipped {
		t.Fatalf("Skipped(pull 5) = %v, %v; want true", skipped, err)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM proposals WHERE proposal_id = 'P1'"); got != "1" {
		t.Fatalf("%s proposals P1; want 1", got)
	}
}
