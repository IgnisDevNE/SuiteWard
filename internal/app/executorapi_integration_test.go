//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// enrollExecutionRepository enrolls an owner with the repository of the executor tests.
func enrollExecutionRepository(t *testing.T, store *postgres.Store) {
	t.Helper()
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, 20), Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: execRepoID, Repo: execRepoName, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
}

// pgExecutorEnv is the executor API over the real execution store, with fakes for what GitHub answers.
type pgExecutorEnv struct {
	t           *testing.T
	databaseURL string
	store       *postgres.Store
	artifacts   *filesystem.Store
	trees       *fakeTrees
	wakes       *wakeLog
	server      *httptest.Server
}

func newPGExecutorEnv(t *testing.T, options ...func(*executorDeps)) *pgExecutorEnv {
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
	enrollExecutionRepository(t, store)
	return serveExecutorAPI(t, databaseURL, cfg.Executor, store, artifacts, options...)
}

// serveExecutorAPI serves the executor API over a database and an artifact directory that the test prepared: the enrolled repository and the jobs are its own.
func serveExecutorAPI(t *testing.T, databaseURL string, times config.ExecutorTimes, store *postgres.Store, artifacts *filesystem.Store, options ...func(*executorDeps)) *pgExecutorEnv {
	t.Helper()
	e := &pgExecutorEnv{t: t, databaseURL: databaseURL, store: store, artifacts: artifacts, wakes: &wakeLog{}, trees: &fakeTrees{entries: map[string][]treeEntry{}, errs: map[string]error{}}}
	var stopping atomic.Bool
	deps := executorDeps{token: []byte(execTestToken), store: store.Execution(), liveness: execution.NewLiveness(time.Now()), times: times, treeTimeout: 5 * time.Second, content: artifacts,
		owners: store, trees: e.trees.read, archives: (&fakeArchives{}).read, wake: e.wakes.wake, stopping: stopping.Load, logger: quietLogger(), now: time.Now}
	for _, option := range options {
		option(&deps)
	}
	handler, err := newExecutorAPI(deps)
	if err != nil {
		t.Fatal(err)
	}
	e.server = httptest.NewServer(newRouter(&api{executor: handler, logger: quietLogger()}))
	t.Cleanup(e.server.Close)
	return e
}

// send is sendRaw with the answer as trimmed text.
func (e *pgExecutorEnv) send(method, path string, payload []byte, lease string) (int, string) {
	e.t.Helper()
	code, got := e.sendRaw(method, path, payload, lease)
	return code, strings.TrimSpace(string(got))
}

// sendRaw sends a request to the executor API and returns the status and the bytes of the answer.
func (e *pgExecutorEnv) sendRaw(method, path string, payload []byte, lease string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.server.URL+"/executor/v1"+path, bytes.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+execTestToken)
	req.Header.Set(leaseHeader, lease)
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is read to its end
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, got
}

// summary is the state, reason and attempts of a job as the database has them.
func (e *pgExecutorEnv) summary(id int64) string {
	return scalar(e.t, e.databaseURL, "SELECT state || ' ' || COALESCE(reason, '-') || ' ' || attempts FROM execution_jobs WHERE job_id = $1", id)
}

// enqueue creates a pending canonical job for the head numbered n, over a manifest that the content store holds.
func (e *pgExecutorEnv) enqueue(n int) execution.Job {
	e.t.Helper()
	return enqueueExecutionJob(e.t, e.store, e.artifacts, n)
}

// enqueueExecutionJob creates a pending canonical job of the enrolled repository for the head numbered n, with the manifest and the files that it names in the content
// store. It is a seed: the job is enqueued directly, so neither the checks job nor the proposal of V1 ran, and these tests exercise the executor API alone. The path
// through the planner, with nothing seeded, is in execution_lease_integration_test.go.
func enqueueExecutionJob(t *testing.T, store *postgres.Store, artifacts *filesystem.Store, n int) execution.Job {
	t.Helper()
	files := map[string]string{"contract.go": fmt.Sprintf("package contract // %d\n", n), declarationPath: "execution:\n  canonical: required\n  profile: go\n"}
	var entries []artifact.Entry
	for path, content := range files {
		if err := artifacts.Put(t.Context(), execDigest(content), strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, artifact.Entry{Path: path, Content: execDigest(content)})
	}
	m, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeManifest(t.Context(), artifacts, m); err != nil { // the seed
		t.Fatal(err)
	}
	job, created, err := store.Execution().Enqueue(t.Context(), execution.NewJob{At: time.Now(), Spec: execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go",
		HeadSHA: execSHA(n), GoverningDigest: m.Digest(), GoverningSource: "V1", DeclarationDigest: execDigest(files[declarationPath]),
		CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff}})
	if err != nil || !created {
		t.Fatalf("enqueue = %v, created %v", err, created)
	}
	return job
}

// The prepare hook runs inside the lease transaction: a tree that cannot be read is recorded and committed while the request is refused, and a
// truncated tree makes its job final while the lease goes on to the next job.
func TestExecutorAPILeaseOnPostgreSQL(t *testing.T) {
	e := newPGExecutorEnv(t)
	unreadable, truncated, good := e.enqueue(1), e.enqueue(2), e.enqueue(3)
	e.trees.fail(execSHA(1), fmt.Errorf("GitHub says no"))
	e.trees.fail(execSHA(2), fmt.Errorf("tree: %w", github.ErrTreeTruncated))

	if code, text := e.send(http.MethodPost, "/lease", nil, ""); code != http.StatusBadGateway || text != `{"error":"github"}` {
		t.Fatalf("first lease = %d %s; want 502 for the tree that cannot be read", code, text)
	}
	if got := e.summary(unreadable.ID); got != "pending github-unavailable 0" {
		t.Errorf("the job whose tree failed = %q; want the failure committed with no attempt", got)
	}
	code, text := e.send(http.MethodPost, "/lease", nil, "") // the unreadable job waits a minute: the truncated one is made final and the good one leased
	var leased struct {
		JobID   int64  `json:"jobId"`
		LeaseID string `json:"leaseId"`
	}
	if err := json.Unmarshal([]byte(text), &leased); code != http.StatusOK || err != nil || leased.JobID != good.ID {
		t.Fatalf("second lease = %d %s (%v); want the job %d", code, text, err, good.ID)
	}
	if got := e.summary(truncated.ID); got != "unavailable too-large 0" {
		t.Errorf("the truncated job = %q; want unavailable for too-large with no attempt", got)
	}
	if got := e.summary(good.ID); got != "leased - 1" {
		t.Errorf("the leased job = %q; want leased once", got)
	}
	if got := scalar(t, e.databaseURL, "SELECT lease_id FROM execution_jobs WHERE job_id = $1", good.ID); got != leased.LeaseID {
		t.Errorf("stored lease = %q; want the lease of the answer, %q", got, leased.LeaseID)
	}
}

// A result is stored with its tests and the states the domain derives, found again when it is sent twice, and a conflict when another body comes.
func TestExecutorAPIResultOnPostgreSQL(t *testing.T) {
	e := newPGExecutorEnv(t)
	job := e.enqueue(1)
	code, text := e.send(http.MethodPost, "/lease", nil, "")
	var leased struct {
		LeaseID string `json:"leaseId"`
	}
	if err := json.Unmarshal([]byte(text), &leased); code != http.StatusOK || err != nil {
		t.Fatalf("lease = %d %s (%v); want 200", code, text, err)
	}
	path := fmt.Sprintf("/jobs/%d", job.ID)
	if code, text := e.send(http.MethodPost, path+"/heartbeat", nil, leased.LeaseID); code != http.StatusOK || !strings.Contains(text, "leaseExpiresAt") {
		t.Fatalf("heartbeat = %d %s; want 200", code, text)
	}
	if code, _ := e.send(http.MethodPost, path+"/heartbeat", nil, strings.Repeat("0", 32)); code != http.StatusConflict {
		t.Errorf("heartbeat of another lease = %d; want 409", code)
	}
	now := time.Now().UTC()
	result := execJSON(t, map[string]any{
		"status": "complete", "head": map[string]any{"sha": job.HeadSHA}, "executor": map[string]any{"version": "0.1.0", "binarySha256": execBinary},
		"sandbox":   map[string]any{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podman 5.5.0"},
		"startedAt": now.Add(-time.Minute).Format(time.RFC3339), "endedAt": now.Format(time.RFC3339),
		"tests": []map[string]any{
			{"package": ".", "name": "TestPasses", "file": "a_test.go", "kind": "declared", "head": []string{"pass"}},
			{"package": ".", "name": "TestFlaky", "file": "a_test.go", "kind": "declared", "head": []string{"fail", "pass"}},
			{"package": ".", "name": "TestTagged", "file": "b_test.go", "kind": "declared", "excluded": "build-constraint"},
		},
	})
	if code, text := e.send(http.MethodPost, path+"/result", result, leased.LeaseID); code != http.StatusOK || text != `{"state":"complete"}` {
		t.Fatalf("result = %d %s; want 200 complete", code, text)
	}
	if got := e.summary(job.ID); got != "complete - 1" {
		t.Errorf("job = %q; want complete", got)
	}
	states := scalar(t, e.databaseURL, "SELECT string_agg(name || '=' || state, ',' ORDER BY name) FROM execution_tests WHERE job_id = $1", job.ID)
	if states != "TestFlaky=flaky,TestPasses=pass,TestTagged=excluded" {
		t.Errorf("stored states = %q; want the ones the domain derives", states)
	}
	if got := scalar(t, e.databaseURL, "SELECT origin || ' ' || executor_version || ' ' || body_digest FROM execution_results WHERE job_id = $1", job.ID); got != "executor 0.1.0 "+artifact.Hash(result).String() {
		t.Errorf("stored result = %q; want the executor's row with the digest of the body", got)
	}
	e.wantReplays(path, result, leased.LeaseID)
}

// wantReplays sends the stored result again, under another lease, and another body for the finished job; the wake-up of the first stays the only one.
func (e *pgExecutorEnv) wantReplays(path string, result []byte, lease string) {
	e.t.Helper()
	if code, text := e.send(http.MethodPost, path+"/result", result, strings.Repeat("0", 32)); code != http.StatusOK || text != `{"state":"complete"}` {
		e.t.Errorf("result sent again = %d %s; want the stored outcome, whatever the lease", code, text)
	}
	other := bytes.Replace(result, []byte(`"0.1.0"`), []byte(`"0.2.0"`), 1)
	if code, text := e.send(http.MethodPost, path+"/result", other, lease); code != http.StatusConflict || text != `{"error":"conflict"}` {
		e.t.Errorf("another result = %d %s; want 409 conflict", code, text)
	}
	if n := e.wakes.count.Load(); n != 1 {
		e.t.Errorf("wake-ups = %d; want one, the first result's", n)
	}
}

// A tree read that never answers ends at the deadline of the tree reads, well before the lease request's: the lease answers 502 and the failure is
// committed, so the GitHub bound can end the job. Before, the one deadline of the request ended the transaction too, and the lease answered 500 with
// nothing recorded, for the same job again at once.
func TestExecutorAPILeaseRecordsATreeReadThatRunsOutOfTimeOnPostgreSQL(t *testing.T) {
	e := newPGExecutorEnv(t, func(d *executorDeps) { d.treeTimeout = 300 * time.Millisecond })
	job := e.enqueue(1)
	e.trees.blockUntilDone(execSHA(1))

	code, text := e.send(http.MethodPost, "/lease", nil, "")

	if code != http.StatusBadGateway || text != `{"error":"github"}` {
		t.Fatalf("lease = %d %s; want 502", code, text)
	}
	if got := e.summary(job.ID); got != "pending github-unavailable 0" {
		t.Errorf("job = %q; want the failure committed with no attempt", got)
	}
	if got := scalar(t, e.databaseURL, "SELECT (github_since IS NOT NULL)::text FROM execution_jobs WHERE job_id = $1", job.ID); got != "true" {
		t.Errorf("github_since set = %s; want the first GitHub failure recorded, which starts the bound", got)
	}
}
