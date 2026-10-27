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

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The executor API over the real execution store: the prepare hook runs inside the lease transaction, a failed GitHub read is recorded and
// committed while the request is refused, a too-large job is made final and the lease goes on to the next job, and a result is stored with its tests
// and found again when it is sent twice.
func TestExecutorAPIOnPostgreSQL(t *testing.T) {
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
		Projects: []owner.ProjectPolicy{{RepositoryID: execRepoID, Repo: execRepoName, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	trees, wakes := &fakeTrees{entries: map[string][]treeEntry{}, errs: map[string]error{}}, &wakeLog{}
	var stopping atomic.Bool
	handler, err := newExecutorAPI(executorDeps{token: []byte(execTestToken), store: store.Execution(), liveness: execution.NewLiveness(time.Now()), times: cfg.Executor, content: artifacts,
		owners: store, trees: trees.read, archives: (&fakeArchives{}).read, wake: wakes.wake, stopping: stopping.Load, logger: quietLogger(), now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newRouter(&api{executor: handler, logger: quietLogger()}))
	t.Cleanup(server.Close)
	send := func(method, path string, payload []byte, lease string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/executor/v1"+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+execTestToken)
		req.Header.Set(leaseHeader, lease)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }() // the body is read to its end
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, strings.TrimSpace(string(got))
	}
	column := func(id int64, name string) string {
		return scalar(t, databaseURL, fmt.Sprintf("SELECT COALESCE(%s::text, '') FROM execution_jobs WHERE job_id = $1", name), id)
	}
	enqueue := func(n int) execution.Job {
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
		if err := storeManifest(t.Context(), artifacts, m); err != nil {
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
	unreadable, truncated, good := enqueue(1), enqueue(2), enqueue(3)
	trees.fail(execSHA(1), fmt.Errorf("GitHub says no"))
	trees.fail(execSHA(2), fmt.Errorf("tree: %w", github.ErrTreeTruncated))

	if code, text := send(http.MethodPost, "/lease", nil, ""); code != http.StatusBadGateway || text != `{"error":"github"}` {
		t.Fatalf("first lease = %d %s; want 502 for the tree that cannot be read", code, text)
	}
	if got := column(unreadable.ID, "state") + " " + column(unreadable.ID, "reason") + " " + column(unreadable.ID, "attempts"); got != "pending github-unavailable 0" {
		t.Errorf("the job whose tree failed = %q; want the failure committed with no attempt", got)
	}
	code, text := send(http.MethodPost, "/lease", nil, "") // the unreadable job waits a minute: the truncated one is made final and the good one leased
	if code != http.StatusOK {
		t.Fatalf("second lease = %d %s; want 200", code, text)
	}
	var leased struct {
		JobID   int64  `json:"jobId"`
		LeaseID string `json:"leaseId"`
	}
	if err := json.Unmarshal([]byte(text), &leased); err != nil || leased.JobID != good.ID {
		t.Fatalf("second lease = %s (%v); want the job %d", text, err, good.ID)
	}
	if got := column(truncated.ID, "state") + " " + column(truncated.ID, "reason") + " " + column(truncated.ID, "attempts"); got != "unavailable too-large 0" {
		t.Errorf("the truncated job = %q; want unavailable for too-large with no attempt", got)
	}
	if got := column(good.ID, "state") + " " + column(good.ID, "attempts") + " " + column(good.ID, "lease_id"); got != "leased 1 "+leased.LeaseID {
		t.Errorf("the leased job = %q; want leased once with the lease of the answer", got)
	}

	jobPath := fmt.Sprintf("/jobs/%d", good.ID)
	if code, text := send(http.MethodPost, jobPath+"/heartbeat", nil, leased.LeaseID); code != http.StatusOK || !strings.Contains(text, "leaseExpiresAt") {
		t.Fatalf("heartbeat = %d %s; want 200", code, text)
	}
	if code, _ := send(http.MethodPost, jobPath+"/heartbeat", nil, strings.Repeat("0", 32)); code != http.StatusConflict {
		t.Errorf("heartbeat of another lease = %d; want 409", code)
	}
	now := time.Now().UTC()
	result := execJSON(t, map[string]any{
		"status": "complete", "head": map[string]any{"sha": good.HeadSHA}, "executor": map[string]any{"version": "0.1.0", "binarySha256": execBinary},
		"sandbox":   map[string]any{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podman 5.5.0"},
		"startedAt": now.Add(-time.Minute).Format(time.RFC3339), "endedAt": now.Format(time.RFC3339),
		"tests": []map[string]any{
			{"package": ".", "name": "TestPasses", "file": "a_test.go", "kind": "declared", "head": []string{"pass"}},
			{"package": ".", "name": "TestFlaky", "file": "a_test.go", "kind": "declared", "head": []string{"fail", "pass"}},
			{"package": ".", "name": "TestTagged", "file": "b_test.go", "kind": "declared", "excluded": "build-constraint"},
		},
	})
	if code, text := send(http.MethodPost, jobPath+"/result", result, leased.LeaseID); code != http.StatusOK || text != `{"state":"complete"}` {
		t.Fatalf("result = %d %s; want 200 complete", code, text)
	}
	if got := column(good.ID, "state"); got != "complete" {
		t.Errorf("state = %q; want complete", got)
	}
	states := scalar(t, databaseURL, "SELECT string_agg(name || '=' || state, ',' ORDER BY name) FROM execution_tests WHERE job_id = $1", good.ID)
	if states != "TestFlaky=flaky,TestPasses=pass,TestTagged=excluded" {
		t.Errorf("stored states = %q; want the ones the domain derives", states)
	}
	if got := scalar(t, databaseURL, "SELECT origin || ' ' || executor_version || ' ' || body_digest FROM execution_results WHERE job_id = $1", good.ID); got != "executor 0.1.0 "+artifact.Hash(result).String() {
		t.Errorf("stored result = %q; want the executor's row with the digest of the body", got)
	}
	if n := wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d; want one", n)
	}
	if code, text := send(http.MethodPost, jobPath+"/result", result, strings.Repeat("0", 32)); code != http.StatusOK || text != `{"state":"complete"}` {
		t.Errorf("result sent again = %d %s; want the stored outcome, whatever the lease", code, text)
	}
	other := bytes.Replace(result, []byte(`"0.1.0"`), []byte(`"0.2.0"`), 1)
	if code, text := send(http.MethodPost, jobPath+"/result", other, leased.LeaseID); code != http.StatusConflict || text != `{"error":"conflict"}` {
		t.Errorf("another result = %d %s; want 409 conflict", code, text)
	}
	if n := wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d after the replays; want still one", n)
	}
}
