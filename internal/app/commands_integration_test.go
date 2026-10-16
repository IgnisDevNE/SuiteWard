//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// TestCommandsJobOnPostgreSQLApprovesRejectsAReusedCodeAndRevokes runs the job against the real stores: the owner's
// approval of the bootstrap proposal, a second command with the same code, and a revocation with a new one. Each
// command leaves its ledger entry and its acknowledgment in the outbox.
func TestCommandsJobOnPostgreSQLApprovesRejectsAReusedCodeAndRevokes(t *testing.T) {
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
	pull := githubsync.ObservedPull{Number: 4, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}
	if _, err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: bootTime, StartedAt: bootTime, Requests: 1, OpenPulls: []githubsync.ObservedPull{pull}, Comments: map[int][]githubsync.ObservedComment{4: nil}}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: newBootRepository(bootRepo, bootRepoID)}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, problems: &bootstrapProblems{},
		logger: logger, now: func() time.Time { return bootTime.Add(time.Hour + 123456789*time.Nanosecond) }}
	if err := bootstrap.run(t.Context()); err != nil { // the Suite
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, bootFiles("v1")) // the proposal P1-R1 that comment 101 approves

	command := func(id int64, created time.Time, verb string) githubsync.ObservedComment {
		code, err := totp.Code(secret, totp.Step(created))
		if err != nil {
			t.Fatal(err)
		}
		return githubsync.ObservedComment{CommentID: id, AuthorID: bootOwnerID, AuthorLogin: "owner", CreatedAt: created, UpdatedAt: created, Body: "/suiteward " + verb + " P1-R1 " + code}
	}
	comments := []githubsync.ObservedComment{command(101, bootTime.Add(10*time.Minute), "approve"), command(102, bootTime.Add(10*time.Minute+5*time.Second), "approve"),
		command(103, bootTime.Add(20*time.Minute), "revoke")}
	if _, err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: bootTime.Add(time.Hour), StartedAt: bootTime.Add(time.Hour), Requests: 1, OpenPulls: []githubsync.ObservedPull{pull},
		Comments: map[int][]githubsync.ObservedComment{4: comments}}); err != nil {
		t.Fatal(err)
	}
	job := &commandsJob{ledger: store, owners: store, suites: store, syncs: store, repos: []string{bootRepo}, logger: logger, now: func() time.Time { return bootTime.Add(2*time.Hour + 123456789*time.Nanosecond) },
		bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil })}
	for range 2 { // the second run finds nothing to decide
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	dispositions := scalar(t, databaseURL, "SELECT string_agg(comment_id || '=' || disposition, ' ' ORDER BY comment_id) FROM pr_commands")
	if want := "101=processed 102=replayed-code 103=processed"; dispositions != want {
		t.Fatalf("ledger = %q; want %q", dispositions, want)
	}
	keys := scalar(t, databaseURL, "SELECT string_agg(key, ' ' ORDER BY key) FROM outbox WHERE key LIKE 'ack:%'")
	if want := "ack:1001:101 ack:1001:102 ack:1001:103"; keys != want {
		t.Fatalf("acknowledgments = %q; want %q", keys, want)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM totp_consumed_steps WHERE source_command_id LIKE 'github-comment:%'"); got != "2" {
		t.Fatalf("%s consumed steps for commands; want 2 (the reused code consumed nothing)", got)
	}
	if strings.Contains(scalar(t, databaseURL, "SELECT string_agg(payload::text, ' ') FROM outbox WHERE key LIKE 'ack:%'"), "/suiteward") {
		t.Fatal("an acknowledgment holds comment text")
	}
	outcomes := map[int64]contract.ConsentOutcome{}
	err = store.Do(t.Context(), owner.ProjectID(bootRepoID), "default", func(ctx context.Context, tx governance.Tx) error {
		for _, id := range []int64{101, 103} {
			receipt, found, err := tx.Receipt(ctx, cjOperation(id))
			if err != nil || !found {
				t.Fatalf("receipt of comment %d: found %v, %v", id, found, err)
			}
			outcomes[id] = receipt.Consent.Result.Outcome()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[101] != contract.ConsentApproved || outcomes[103] != contract.ConsentRevoked {
		t.Fatalf("outcomes = %v; want approved then revoked", outcomes)
	}
}
