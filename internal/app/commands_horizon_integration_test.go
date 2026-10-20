//go:build integration

package app

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// TestCommandsJobOnPostgreSQLDecidesACommentOnlyOnceACycleStartedTenSecondsAfterIt runs the commands job over the real
// stores: the horizon is the repository's CycleStartedAt, GitHub's clock, minus 10 s, so a comment created at T waits
// for a cycle that started after T+10 s (docs/contracts/runtime.md, "Commands horizon").
func TestCommandsJobOnPostgreSQLDecidesACommentOnlyOnceACycleStartedTenSecondsAfterIt(t *testing.T) {
	databaseURL := newDatabase(t)
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(t.Context(), databaseURL); err != nil {
		t.Fatal(err)
	}
	pool, store, _, err := connectWithArtifacts(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, totp.SecretBytes), Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	created := bootTime.Add(10 * time.Minute) // T
	pull := githubsync.ObservedPull{Number: 4, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}
	comment := githubsync.ObservedComment{CommentID: 101, AuthorID: bootOwnerID, AuthorLogin: "owner", CreatedAt: created, UpdatedAt: created, Body: "not a command"}
	cycle := func(started time.Time) {
		t.Helper()
		// At is long after StartedAt: the horizon must follow GitHub's start of the cycle, not the time it ended.
		if _, err := store.RecordCycle(t.Context(), githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: started.Add(time.Hour), StartedAt: started, Requests: 1,
			OpenPulls: []githubsync.ObservedPull{pull}, Comments: map[int][]githubsync.ObservedComment{4: {comment}}}); err != nil {
			t.Fatal(err)
		}
	}
	job := &commandsJob{ledger: store, owners: store, suites: store, syncs: store, pulls: store, repos: []string{bootRepo}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time { return created.Add(3 * time.Hour) }, bot: newBotAccount(func(context.Context) (int64, error) { return cmtBotID, nil })}
	decided := func() string {
		return scalar(t, databaseURL, "SELECT count(*)::text FROM pr_commands WHERE comment_id = 101")
	}

	cycle(created.Add(9 * time.Second)) // the horizon is T-1 s: the comment is not past it
	if wake, err := job.run(t.Context()); err != nil || len(wake) != 0 || decided() != "0" {
		t.Fatalf("after a cycle started at T+9 s: wake %v, err %v, %s entries; want no wake-up and the comment pending", wake, err, decided())
	}

	cycle(created.Add(11 * time.Second)) // the horizon is T+1 s
	wake, err := job.run(t.Context())
	if want := (river.Wake{river.ChecksKind, river.BootstrapKind}); err != nil || !slices.Equal(wake, want) || decided() != "1" {
		t.Fatalf("after a cycle started at T+11 s: wake %v, err %v, %s entries; want %v and the comment decided", wake, err, decided(), want)
	}
}
