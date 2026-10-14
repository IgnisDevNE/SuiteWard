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
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// TestBootstrapJobOnPostgreSQLRunsTheFullSequence runs the bootstrap against the real store: the Suite, the
// proposal with its summary, the owner's approval, the assessment and the first canonical with its promoted
// message, each written once however often the job runs.
func TestBootstrapJobOnPostgreSQLRunsTheFullSequence(t *testing.T) {
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
	count := func(query string) string { return scalar(t, databaseURL, query) }

	for range 2 {
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := count("SELECT count(*)::text FROM outbox WHERE key = 'bootstrap-summary:1001:P1-R1' AND kind = 'github_comment'"); got != "1" {
		t.Fatalf("%s summary messages, want 1", got)
	}

	reference := contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: "consent:github-comment:101", SourceCommandID: "github-comment:101", Actor: bootOwnerPrincipal(),
		Reference: reference, Carrier: "github-pr:1001/4", Action: contract.ApproveConsent, Order: 101}))
	if response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil || !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("approve: eligible %v, %v", response.Receipt.CurrentApprovalEligible, err)
	}
	for range 3 {
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	var version contract.SuiteVersionID
	err = store.Do(t.Context(), owner.ProjectID(bootRepoID), "default", func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		version, _ = state.Canonical.Suite().CurrentVersionID()
		return err
	})
	if err != nil || version != "V1" {
		t.Fatalf("canonical = %q, %v; want V1", version, err)
	}
	if got := count("SELECT count(*)::text FROM outbox WHERE key = 'bootstrap-promoted:1001:P1-R1' AND kind = 'github_comment'"); got != "1" {
		t.Fatalf("%s promoted messages, want 1", got)
	}
	if got := count("SELECT count(*)::text FROM outbox WHERE kind = 'github_comment'"); got != "2" {
		t.Fatalf("%s github_comment messages, want exactly the summary and the promotion", got)
	}
}
