//go:build integration

package app

import (
	"context"
	"fmt"
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
	job := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, content: artifacts, problems: &bootstrapProblems{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return bootTime.Add(time.Hour + 123456789*time.Nanosecond) }}
	count := func(query string) string { return scalar(t, databaseURL, query) }

	if err := job.run(t.Context()); err != nil { // creates the Suite and proposes nothing
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, bootFiles("v1")) // as the commands job would
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

// TestBootstrapJobOnPostgreSQLBootstrapsEveryRepository pins that two enrolled repositories each promote their
// first baseline: operation ids are global, so the id carries the repository id.
func TestBootstrapJobOnPostgreSQLBootstrapsEveryRepository(t *testing.T) {
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
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}, {RepositoryID: bootOtherRepoID, Repo: bootOtherRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	for i, repo := range []string{bootRepo, bootOtherRepo} {
		pull := 4 + i
		cycle := githubsync.Cycle{Repo: repo, InstallationID: 1, At: bootTime, Comments: map[int][]githubsync.ObservedComment{pull: nil},
			OpenPulls: []githubsync.ObservedPull{{Number: pull, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}}}
		if err := store.RecordCycle(t.Context(), cycle); err != nil {
			t.Fatal(err)
		}
	}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: newBootRepository(bootRepo, bootRepoID), bootOtherRepo: newBootRepository(bootOtherRepo, bootOtherRepoID)}, calls: map[string]int{}}
	job := &bootstrapJob{repos: []string{bootRepo, bootOtherRepo}, reader: reader, owners: store, suites: store, content: artifacts, problems: &bootstrapProblems{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return bootTime.Add(time.Hour) }}
	if err := job.run(t.Context()); err != nil { // creates both Suites
		t.Fatal(err)
	}
	for i, id := range []int64{bootRepoID, bootOtherRepoID} {
		proposeBaseline(t, store, artifacts, id, []string{bootRepo, bootOtherRepo}[i], contract.OriginBranch, 4+i, bootHead, bootFiles("v1")) // as the commands job would
		reference := contract.ProposalReference{ProjectID: owner.ProjectID(id), SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
		command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(fmt.Sprintf("consent:github-comment:%d", 101+i)), SourceCommandID: contract.SourceCommandID(fmt.Sprintf("github-comment:%d", 101+i)),
			Actor: bootOwnerPrincipal(), Reference: reference, Carrier: contract.ApprovalCarrierID(fmt.Sprintf("github-pr:%d/%d", id, 4+i)), Action: contract.ApproveConsent, Order: contract.CommandOrder(101 + i)}))
		if response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil || !response.Receipt.CurrentApprovalEligible {
			t.Fatalf("approve %d: eligible %v, %v", id, response.Receipt.CurrentApprovalEligible, err)
		}
	}
	if err := job.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{bootRepoID, bootOtherRepoID} {
		var version contract.SuiteVersionID
		err = store.Do(t.Context(), owner.ProjectID(id), "default", func(ctx context.Context, tx governance.Tx) error {
			state, err := tx.Suite(ctx)
			version, _ = state.Canonical.Suite().CurrentVersionID()
			return err
		})
		if err != nil || version != "V1" {
			t.Fatalf("repository %d: canonical = %q, %v; want V1", id, version, err)
		}
	}
}
