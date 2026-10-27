//go:build integration

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// seedExecutionJobs enrolls the repository of the executor tests and creates a pending canonical job for each head number, in the database and the
// artifact directory of vars, before the service that serves them starts.
func seedExecutionJobs(t *testing.T, vars map[string]string, heads ...int) []execution.Job {
	t.Helper()
	env := testEnv(vars)
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	pool, store, artifacts, err := connectWithArtifacts(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	enrollExecutionRepository(t, store)
	jobs := make([]execution.Job, len(heads))
	for i, n := range heads {
		jobs[i] = enqueueExecutionJob(t, store, artifacts, n)
	}
	return jobs
}

// executor sends a request to the executor API of the running service, with the token unless it is empty.
func (s *service) executor(method, path, token, lease string, body []byte) (int, string) {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), method, "http://"+s.addr+"/executor/v1"+path, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set(leaseHeader, lease)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is read to its end
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp.StatusCode, string(got)
}

// executionBlock is the execution block of /status.
func (s *service) executionBlock() map[string]any {
	s.t.Helper()
	block, ok := s.status()["execution"].(map[string]any)
	if !ok {
		s.t.Fatalf("GET /status has no execution block: %v", s.status())
	}
	return block
}

// Without an executor token nothing is served under /executor/v1, whatever the request carries.
func TestServeDoesNotServeTheExecutorAPIWithoutAToken(t *testing.T) {
	s := startRun(t, serveVars(t, newDatabase(t)))
	s.waitReady()

	for _, token := range []string{"", execTestToken} {
		if code, body := s.executor(http.MethodPost, "/lease", token, "", nil); code != http.StatusNotFound || body != "" {
			t.Errorf("POST /executor/v1/lease with token %q = %d %q; want 404 with an empty body", token, code, body)
		}
	}
	if block := s.executionBlock(); block["enabled"] != false {
		t.Errorf("status.execution = %v; want enabled false", block)
	}
}

// With a token and no GitHub settings the API is served all the same, and a lease can read no tree: 502, and the job stays pending with its reason.
func TestServeServesTheExecutorAPIWithATokenAndNoGitHub(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	vars["SUITEWARD_EXECUTOR_TOKEN"] = execTestToken
	jobs := seedExecutionJobs(t, vars, 1)
	s := startRun(t, vars)
	s.waitReady()

	if code, _ := s.executor(http.MethodPost, "/lease", "", "", nil); code != http.StatusUnauthorized {
		t.Errorf("POST /lease without the token = %d; want 401", code)
	}
	if code, body := s.executor(http.MethodPost, "/lease", execTestToken, "", nil); code != http.StatusBadGateway || strings.TrimSpace(body) != `{"error":"github"}` {
		t.Fatalf("POST /lease = %d %s; want 502, with no GitHub to read the tree from", code, body)
	}
	got := scalar(t, databaseURL, "SELECT state || ' ' || COALESCE(reason, '-') || ' ' || attempts FROM execution_jobs WHERE job_id = $1", jobs[0].ID)
	if got != "pending github-unavailable 0" {
		t.Errorf("job = %q; want pending with github-unavailable and no attempt", got)
	}
	block := s.executionBlock()
	if jobsBy, _ := block["jobs"].(map[string]any); block["enabled"] != true || jobsBy["pending"] != float64(1) {
		t.Errorf("status.execution = %v; want enabled true and one pending job", block)
	}
}

// executorGitHubFake is a GitHub for the whole executor path: tokens, the tree of a commit, and its archive behind a redirect.
type executorGitHubFake struct {
	mu        sync.Mutex
	tokenMaps []map[string]string
	server    *httptest.Server
}

func (f *executorGitHubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head := execSHA(1)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", setupInstallID):
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.tokenMaps = append(f.tokenMaps, body.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": body.Permissions})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/git/trees/"+head:
		_, _ = fmt.Fprintf(w, `{"sha":%q,"truncated":false,"tree":[`+
			`{"path":"contract.go","mode":"100664","type":"blob","sha":%q},`+
			`{"path":"pkg","mode":"040000","type":"tree","sha":%q},`+
			`{"path":"pkg/run.sh","mode":"100755","type":"blob","sha":%q},`+
			`{"path":"vendor/lib","mode":"160000","type":"commit","sha":%q}]}`, head, strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/tarball/"+head:
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.Header().Set("Location", f.server.URL+"/codeload/"+head+".tar.gz")
		w.WriteHeader(http.StatusFound)
	case r.Method == http.MethodGet && r.URL.Path == "/codeload/"+head+".tar.gz":
		_, _ = w.Write([]byte("the archive of the head"))
	default:
		http.NotFound(w, r)
	}
}

// With GitHub configured, the lease lists the tree that the sync reader reads (100664 and the submodule included, the directory left out), the archive
// is streamed from the archive reader's request, and a result ends the job.
func TestServeServesTheExecutorAPIOverGitHub(t *testing.T) {
	fake := &executorGitHubFake{}
	fake.server = httptest.NewServer(fake)
	t.Cleanup(fake.server.Close)
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	vars["SUITEWARD_EXECUTOR_TOKEN"] = execTestToken
	for name, value := range map[string]string{"SUITEWARD_GITHUB_APP_ID": "1", "SUITEWARD_GITHUB_APP_KEY": setupKey(), "SUITEWARD_GITHUB_INSTALLATION_ID": "42",
		"SUITEWARD_GITHUB_REPOSITORIES": execRepoName, "SUITEWARD_GITHUB_API_URL": fake.server.URL} {
		vars[name] = value
	}
	jobs := seedExecutionJobs(t, vars, 1)
	s := startRun(t, vars)
	s.waitReady()

	code, text := s.executor(http.MethodPost, "/lease", execTestToken, "", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /lease = %d %s; want 200", code, text)
	}
	var lease execLease
	if err := json.Unmarshal([]byte(text), &lease); err != nil || lease.JobID != jobs[0].ID || lease.Head == nil {
		t.Fatalf("lease = %s (%v); want the job with its head", text, err)
	}
	wantTree := []treeEntry{{Path: "contract.go", Mode: "100664", SHA: strings.Repeat("a", 40)}, {Path: "pkg/run.sh", Mode: "100755", SHA: strings.Repeat("c", 40)}, {Path: "vendor/lib", Mode: "160000", SHA: strings.Repeat("d", 40)}}
	if len(lease.Head.Tree) != len(wantTree) {
		t.Fatalf("head tree = %+v; want %+v", lease.Head.Tree, wantTree)
	}
	for i, want := range wantTree {
		if lease.Head.Tree[i] != want {
			t.Errorf("head tree[%d] = %+v; want %+v", i, lease.Head.Tree[i], want)
		}
	}
	path := fmt.Sprintf("/jobs/%d", jobs[0].ID)
	if code, body := s.executor(http.MethodGet, path+"/archive/head", execTestToken, lease.LeaseID, nil); code != http.StatusOK || body != "the archive of the head" {
		t.Errorf("archive = %d %q; want 200 with the archive host's bytes", code, body)
	}
	now := time.Now().UTC()
	result := execJSON(t, map[string]any{
		"status": "complete", "head": map[string]any{"sha": jobs[0].HeadSHA}, "executor": map[string]any{"version": "0.1.0", "binarySha256": execBinary},
		"sandbox":   map[string]any{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podman 5.5.0"},
		"startedAt": now.Add(-time.Minute).Format(time.RFC3339), "endedAt": now.Format(time.RFC3339),
		"tests": []map[string]any{{"package": ".", "name": "TestContract", "file": "contract_test.go", "kind": "declared", "head": []string{"pass"}}},
	})
	if code, body := s.executor(http.MethodPost, path+"/result", execTestToken, lease.LeaseID, result); code != http.StatusOK || strings.TrimSpace(body) != `{"state":"complete"}` {
		t.Fatalf("result = %d %s; want 200 complete", code, body)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	archiveToken := false
	for _, m := range fake.tokenMaps {
		archiveToken = archiveToken || (len(m) == 2 && m["metadata"] == "read" && m["contents"] == "read")
	}
	if !archiveToken {
		t.Errorf("tokens asked for %v; want one with exactly metadata read and contents read for the archive", fake.tokenMaps)
	}
	if block := s.executionBlock(); block["enabled"] != true {
		t.Errorf("status.execution = %v; want enabled", block)
	}
}
