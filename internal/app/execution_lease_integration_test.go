//go:build integration

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// bootstrappedProject is a project whose V1 the bootstrap promoted from the default branch (docs/install.md, case 1), over the real stores. Nothing seeds the
// content store: what a lease reads from it is what the application wrote there.
type bootstrappedProject struct {
	t           *testing.T
	databaseURL string
	artifactDir string
	times       config.ExecutorTimes
	store       *postgres.Store
	artifacts   *filesystem.Store
	reader      *fakeBootReader
	job         *checksJob
	clock       time.Time
}

// newBootstrappedProject promotes V1 from the files of the default branch with the bootstrap job: the Suite, the proposal of the commands job, the owner's
// consent and the first canonical. Pull request 4 is open at the head of the default branch.
func newBootstrappedProject(t *testing.T, files map[string]string) *bootstrappedProject {
	t.Helper()
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	env := testEnv(vars)
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
	p := &bootstrappedProject{t: t, databaseURL: databaseURL, artifactDir: vars["SUITEWARD_ARTIFACT_DIR"], times: cfg.Executor, store: store, artifacts: artifacts, clock: bootTime.Add(2 * time.Hour)}
	p.sync(bootTime, map[int]string{4: bootHead})
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

// sync records a synchronization cycle in which the pull requests are open at their heads.
func (p *bootstrappedProject) sync(at time.Time, heads map[int]string) {
	p.t.Helper()
	c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
	for number, head := range heads {
		c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime})
		c.Comments[number] = nil
	}
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

// onlyJob is the one job of a head for a plan.
func (p *bootstrappedProject) onlyJob(pull int, head string, plan execution.Plan) execution.Job {
	p.t.Helper()
	jobs, err := p.store.Execution().HeadJobs(p.t.Context(), bootRepoID, pull, head, plan)
	if err != nil || len(jobs) != 1 {
		p.t.Fatalf("%s jobs of pull %d: %v, error %v; want one", plan, pull, jobs, err)
	}
	return jobs[0]
}

// manifestObject is the file of the content store that holds the manifest of the job.
func (p *bootstrappedProject) manifestObject(job execution.Job) string {
	return filepath.Join(p.artifactDir, "sha256-"+strings.TrimPrefix(job.GoverningDigest.String(), "sha256:"))
}

// wantLeased leases through the executor API, which must answer with job and its governing manifest, and downloads every file that the manifest lists.
func (p *bootstrappedProject) wantLeased(job execution.Job, files int) {
	p.t.Helper()
	e := serveExecutorAPI(p.t, p.databaseURL, p.times, p.store, p.artifacts)
	code, text := e.send(http.MethodPost, "/lease", nil, "")
	var leased leaseAnswer
	if err := json.Unmarshal([]byte(text), &leased); code != http.StatusOK || err != nil || leased.JobID != job.ID {
		p.t.Fatalf("lease = %d %s (%v); want 200 with the job %d", code, text, err, job.ID)
	}
	if leased.Manifest.Source != string(job.GoverningSource) || leased.Manifest.Digest != job.GoverningDigest.String() || len(leased.Manifest.Files) != files {
		p.t.Errorf("manifest of the lease = %+v; want %s, %s and its %d files", leased.Manifest, job.GoverningSource, job.GoverningDigest, files)
	}
	for _, file := range leased.Manifest.Files {
		code, body := e.sendRaw(http.MethodGet, fmt.Sprintf("/jobs/%d/files/%s", job.ID, file.Digest), nil, leased.LeaseID)
		if code != http.StatusOK || artifact.Hash(body).String() != file.Digest {
			p.t.Errorf("file %s = %d, %d bytes; want 200 with the bytes of %s", file.Path, code, len(body), file.Digest)
		}
	}
}

// A canonical job that the checks job creates for the V1 of the bootstrap leases: the lease and the download of its files read the governing manifest from
// the content store, so the checks job puts it there when it plans the job. The manifest of a V1 that the bootstrap promoted is in the database only, and
// no revision was ever recorded for it.
func TestTheCanonicalJobOfABootstrappedV1LeasesAndServesItsFilesOnPostgreSQL(t *testing.T) {
	files := execHeadFiles(execDeclaration("required", "off"), "v1")
	p := newBootstrappedProject(t, files)
	p.step()

	p.wantLeased(p.onlyJob(4, bootHead, execution.PlanCanonical), len(files))
}

// The receipt job of a revision names the manifest of the canonical version that the revision expects. With the canonical level off no canonical job puts V1
// in the store, so the receipt plan must.
func TestTheReceiptJobOfARevisionLeasesAndServesTheFilesOfItsCanonicalOnPostgreSQL(t *testing.T) {
	declaration := execDeclaration("off", "required")
	p := newBootstrappedProject(t, execHeadFiles(declaration, "v1"))
	p.reader.repo(bootRepo).trees[cjHead2] = &bootTree{files: execHeadFiles(declaration, "v2")}
	p.sync(p.clock, map[int]string{4: bootHead, 5: cjHead2})
	p.step()

	p.wantLeased(p.onlyJob(5, cjHead2, execution.PlanReceipt), len(execHeadFiles(declaration, "v1")))
}

// A job that exists already is healed by the next run: the object that its governing manifest names is in the store again, as it is for a job that an
// earlier version created, with no manifest, or whose object was lost.
func TestTheNextRunPutsTheManifestOfAnOpenJobBackOnPostgreSQL(t *testing.T) {
	files := execHeadFiles(execDeclaration("required", "off"), "v1")
	p := newBootstrappedProject(t, files)
	p.step()
	job := p.onlyJob(4, bootHead, execution.PlanCanonical)
	if err := os.Remove(p.manifestObject(job)); err != nil && !errors.Is(err, fs.ErrNotExist) { // before the fix the object was never there
		t.Fatal(err)
	}

	p.step()

	if got := p.onlyJob(4, bootHead, execution.PlanCanonical); got.ID != job.ID {
		t.Fatalf("the job is %d after the run, was %d: the run must keep it, not make another", got.ID, job.ID)
	}
	p.wantLeased(job, len(files))
}
