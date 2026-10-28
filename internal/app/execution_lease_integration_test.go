//go:build integration

package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// bootstrappedProject is a project whose V1 the bootstrap promoted from the default branch (docs/install.md, case 1), over the real stores. Nothing seeds
// the content store: what a lease reads from it is what the application wrote there.
type bootstrappedProject struct {
	t           *testing.T
	databaseURL string
	times       config.ExecutorTimes
	store       *postgres.Store
	artifacts   *filesystem.Store
	reader      *fakeBootReader
	job         *checksJob
	clock       time.Time
}

// newBootstrappedProject promotes V1 from the files of the default branch with the bootstrap job: the Suite, the proposal of the commands job, the owner's
// consent and the first canonical. A pull request is open at the head of the default branch.
func newBootstrappedProject(t *testing.T, files map[string]string) *bootstrappedProject {
	t.Helper()
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
	p := &bootstrappedProject{t: t, databaseURL: databaseURL, times: cfg.Executor, store: store, artifacts: artifacts, clock: bootTime.Add(2 * time.Hour)}
	p.sync(bootTime)
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[bootHead] = &bootTree{files: files}
	p.reader = &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: p.reader, owners: store, suites: store, problems: &bootstrapProblems{}, logger: logger,
		now: func() time.Time { return bootTime.Add(time.Hour) }}
	if _, err := bootstrap.run(t.Context()); err != nil { // the Suite
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, files)
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: "consent:github-comment:101", SourceCommandID: "github-comment:101", Actor: bootOwnerPrincipal(),
		Action: contract.ApproveConsent, Order: testOrder(101), Carrier: carrierID(bootRepoID, 4),
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(1), RevisionID: "R1"}}))
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil {
		t.Fatal(err)
	}
	for range 3 { // the integrity assessment and the first canonical
		if _, err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	p.job = (&checksJob{repos: []string{bootRepo}, reader: mergeBaseReader{p.reader}, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(),
		integrity: store.Integrity(), commands: store, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return p.clock }}).
		withExecution(store.Execution(), store.ReceiptChecks(), p.clock)
	return p
}

// sync records a synchronization cycle in which the pull request 4 is open at the head of the default branch.
func (p *bootstrappedProject) sync(at time.Time) {
	p.t.Helper()
	c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{4: nil},
		OpenPulls: []githubsync.ObservedPull{{Number: 4, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}}}
	if _, err := p.store.RecordCycle(p.t.Context(), c); err != nil {
		p.t.Fatal(err)
	}
}

// step runs the checks job once, a minute later, with an executor that is seen at that time.
func (p *bootstrappedProject) step() {
	p.t.Helper()
	p.clock = p.clock.Add(time.Minute)
	p.job.liveness.Saw(p.clock)
	if err := p.job.run(p.t.Context()); err != nil {
		p.t.Fatal(err)
	}
}

// canonicalJob is the one canonical job of the pull request 4.
func (p *bootstrappedProject) canonicalJob() execution.Job {
	p.t.Helper()
	jobs, err := p.store.Execution().HeadJobs(p.t.Context(), bootRepoID, 4, bootHead, execution.PlanCanonical)
	if err != nil || len(jobs) != 1 {
		p.t.Fatalf("canonical jobs of pull 4: %v, error %v; want one", jobs, err)
	}
	return jobs[0]
}

// A canonical job that the checks job creates for the V1 of the bootstrap leases: the lease and the download of its files read the governing manifest from
// the content store, so the checks job puts it there when it plans the job. The manifest of a V1 that the bootstrap promoted is in the database only, and
// no revision was ever recorded for it.
func TestTheCanonicalJobOfABootstrappedV1LeasesAndServesItsFilesOnPostgreSQL(t *testing.T) {
	files := execHeadFiles(execDeclaration("required", "off"), "v1")
	p := newBootstrappedProject(t, files)
	p.step()
	job := p.canonicalJob()
	e := serveExecutorAPI(t, p.databaseURL, p.times, p.store, p.artifacts)

	code, text := e.send(http.MethodPost, "/lease", nil, "")

	var leased leaseAnswer
	if err := json.Unmarshal([]byte(text), &leased); code != http.StatusOK || err != nil || leased.JobID != job.ID {
		t.Fatalf("lease = %d %s (%v); want 200 with the job %d", code, text, err, job.ID)
	}
	if leased.Manifest.Source != "V1" || leased.Manifest.Digest != job.GoverningDigest.String() || len(leased.Manifest.Files) != len(files) {
		t.Errorf("manifest of the lease = %+v; want V1, %s and its %d files", leased.Manifest, job.GoverningDigest, len(files))
	}
	for _, file := range leased.Manifest.Files {
		code, body := e.sendRaw(http.MethodGet, fmt.Sprintf("/jobs/%d/files/%s", job.ID, file.Digest), nil, leased.LeaseID)
		if code != http.StatusOK || artifact.Hash(body).String() != file.Digest {
			t.Errorf("file %s = %d, %d bytes; want 200 with the bytes of %s", file.Path, code, len(body), file.Digest)
		}
	}
}
