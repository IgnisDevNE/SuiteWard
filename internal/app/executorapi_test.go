package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// execTestToken is a fixture, not a credential.
const execTestToken = "test-token-0123456789-abcdefghijklmnop"

// The repository of every job of these tests: the one enrolledStore enrolls.
const (
	execRepoID   = 10
	execRepoName = "acme/widgets"
)

// execClock is the clock of the handler and of the jobs it leases.
type execClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *execClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *execClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// lockedBuffer is a log destination that handler goroutines and the test read and write.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeTrees answers the tree reads of the lease: a single entry unless a test sets the tree or the failure of a commit.
type fakeTrees struct {
	mu      sync.Mutex
	entries map[string][]treeEntry
	errs    map[string]error
	calls   []treeCall
}

type treeCall struct {
	repo, sha string
	deadline  bool
}

func (f *fakeTrees) set(sha string, entries []treeEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[sha] = entries
}

func (f *fakeTrees) fail(sha string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[sha] = err
}

func (f *fakeTrees) read(ctx context.Context, repo, sha string) ([]treeEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, bounded := ctx.Deadline()
	f.calls = append(f.calls, treeCall{repo: repo, sha: sha, deadline: bounded})
	if err := f.errs[sha]; err != nil {
		return nil, err
	}
	if entries, ok := f.entries[sha]; ok {
		return entries, nil
	}
	return []treeEntry{{Path: "a.go", Mode: "100644", SHA: strings.Repeat("a", 40)}}, nil
}

func (f *fakeTrees) asked() []treeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]treeCall(nil), f.calls...)
}

// archiveCall is one request of the archive reader.
type archiveCall struct {
	repo, sha string
	max       int64
	deadline  time.Time
}

// fakeArchives stands for the GitHub archive read: it writes "archive of <sha>" unless a test sets what it does.
type fakeArchives struct {
	mu    sync.Mutex
	run   func(ctx context.Context, dst io.Writer) (int64, error)
	calls []archiveCall
}

func (f *fakeArchives) set(run func(ctx context.Context, dst io.Writer) (int64, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = run
}

func (f *fakeArchives) read(ctx context.Context, repo, sha string, dst io.Writer, maxBytes int64) (int64, error) {
	f.mu.Lock()
	deadline, _ := ctx.Deadline()
	f.calls = append(f.calls, archiveCall{repo: repo, sha: sha, max: maxBytes, deadline: deadline})
	run := f.run
	f.mu.Unlock()
	if run != nil {
		return run(ctx, dst)
	}
	n, err := io.WriteString(dst, "archive of "+sha)
	return int64(n), err
}

func (f *fakeArchives) asked() []archiveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]archiveCall(nil), f.calls...)
}

// wakeLog records the wake-ups of github_checks and fails them when err is set.
type wakeLog struct {
	count atomic.Int32
	err   error
}

func (w *wakeLog) wake(context.Context) error {
	w.count.Add(1)
	return w.err
}

// execTestEnv is an executor API over the in-memory execution store, the filesystem content store and fakes for GitHub.
type execTestEnv struct {
	t        *testing.T
	clock    *execClock
	store    *executiontest.Memory
	liveness *execution.Liveness
	content  *filesystem.Store
	trees    *fakeTrees
	archives *fakeArchives
	logs     *lockedBuffer
	wakes    *wakeLog
	stopping atomic.Bool
	times    config.ExecutorTimes
	server   *httptest.Server
}

var execOrigin = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func newExecEnv(t *testing.T) *execTestEnv {
	t.Helper()
	artifacts, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &execTestEnv{t: t, clock: &execClock{now: execOrigin}, content: artifacts, logs: &lockedBuffer{}, wakes: &wakeLog{},
		trees: &fakeTrees{entries: map[string][]treeEntry{}, errs: map[string]error{}}, archives: &fakeArchives{},
		times:    config.ExecutorTimes{JobTimeout: 30 * time.Minute, LeaseWait: 5 * time.Minute, GitHubWait: 30 * time.Minute},
		store:    executiontest.NewMemory(commandstest.NewMemory(ownertest.NewMemory(), githubsynctest.NewMemory())),
		liveness: execution.NewLiveness(execOrigin.Add(-time.Hour))}
	handler, err := newExecutorAPI(executorDeps{token: []byte(execTestToken), store: e.store, liveness: e.liveness, times: e.times, content: artifacts,
		owners: enrolledStore(t, "isolated"), trees: e.trees.read, archives: e.archives.read, wake: e.wakes.wake, stopping: e.stopping.Load,
		logger: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), now: e.clock.Now})
	if err != nil {
		t.Fatalf("compose the executor API: %v", err)
	}
	e.server = httptest.NewServer(newRouter(&api{executor: handler, logger: quietLogger()}))
	t.Cleanup(e.server.Close)
	return e
}

// execReply is an answer of the API.
type execReply struct {
	status int
	header http.Header
	body   []byte
}

func (r execReply) text() string { return string(r.body) }

// execMod changes a request before it is sent.
type execMod func(*http.Request)

func withToken(token string) execMod {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func withoutAuthorization() execMod { return func(r *http.Request) { r.Header.Del("Authorization") } }

func withLease(id string) execMod {
	return func(r *http.Request) { r.Header.Set("Suiteward-Lease", id) }
}

func withHeader(name, value string) execMod {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

// send sends a request to the API with the right token unless a modification replaces it.
func (e *execTestEnv) send(method, path string, body []byte, mods ...execMod) execReply {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.server.URL+"/executor/v1"+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+execTestToken)
	for _, mod := range mods {
		mod(req)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }() // the body was read to its end or the test has failed
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read the answer of %s %s: %v", method, path, err)
	}
	return execReply{status: resp.StatusCode, header: resp.Header, body: got}
}

func (e *execTestEnv) wantError(r execReply, status int, code string) {
	e.t.Helper()
	if r.status != status || strings.TrimSpace(r.text()) != fmt.Sprintf(`{"error":%q}`, code) {
		e.t.Errorf("answer = %d %s; want %d {\"error\":%q}", r.status, r.text(), status, code)
	}
}

// Seeding: a job's manifest and the files it names are in the content store, as the checks job leaves them.

func execDigest(s string) artifact.Digest { return artifact.Hash([]byte(s)) }

func execSHA(n int) string { return fmt.Sprintf("%040x", n) }

// manifest stores the files and the manifest that names them, and returns the manifest.
func (e *execTestEnv) manifest(files map[string]string) artifact.Manifest {
	e.t.Helper()
	var entries []artifact.Entry
	for path, content := range files {
		e.put(content)
		entries = append(entries, artifact.Entry{Path: path, Content: execDigest(content)})
	}
	m, err := artifact.NewManifest(entries)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.content.Put(e.t.Context(), m.Digest(), bytes.NewReader(m.Encoding())); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *execTestEnv) put(content string) artifact.Digest {
	e.t.Helper()
	d := execDigest(content)
	if err := e.content.Put(e.t.Context(), d, strings.NewReader(content)); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *execTestEnv) enqueue(spec execution.Spec) execution.Job {
	e.t.Helper()
	job, created, err := e.store.Enqueue(e.t.Context(), execution.NewJob{Spec: spec, At: e.clock.Now()})
	if err != nil || !created {
		e.t.Fatalf("enqueue %+v = %v, created %v", spec, err, created)
	}
	return job
}

// defaultFiles is the manifest of the jobs that name none.
var defaultFiles = map[string]string{"contract.go": "package contract\n", ".suiteward.yml": "execution:\n  canonical: required\n  profile: go\n"}

// canonicalJob is a pending canonical job for the head numbered n over the manifest of files.
func (e *execTestEnv) canonicalJob(n int, files map[string]string) execution.Job {
	e.t.Helper()
	m := e.manifest(files)
	return e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(n),
		GoverningDigest: m.Digest(), GoverningSource: "V1", DeclarationDigest: execDigest(files[".suiteward.yml"]),
		CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
}

// receiptJob is a pending receipt job for the head numbered n and the base numbered n+1000 over the manifest of files.
func (e *execTestEnv) receiptJob(n int, files map[string]string) execution.Job {
	e.t.Helper()
	m := e.manifest(files)
	return e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanReceipt, Profile: "go", HeadSHA: execSHA(n), BaseSHA: execSHA(n + 1000),
		GoverningDigest: m.Digest(), GoverningSource: "P1-R2", DeclarationDigest: execDigest(files[".suiteward.yml"]),
		CanonicalLevel: domain.CanonicalLevelOff, ReceiptLevel: domain.ReceiptLevelAdvisory})
}

// grant leases the oldest ready job straight from the store, as the lease route does, and returns it with its lease id.
func (e *execTestEnv) grant() execution.Job {
	e.t.Helper()
	req := execution.LeaseRequest{Now: e.clock.Now(), LeaseID: fmt.Sprintf("%032x", e.clock.Now().UnixNano()), JobTimeout: e.times.JobTimeout, GitHubWait: e.times.GitHubWait}
	job, granted, err := e.store.Lease(e.t.Context(), req, func(context.Context, execution.Job) error { return nil })
	if err != nil || !granted {
		e.t.Fatalf("lease = %v, granted %v", err, granted)
	}
	return job
}

func (e *execTestEnv) job(id int64) execution.Job {
	e.t.Helper()
	job, found, err := e.store.Job(e.t.Context(), id)
	if err != nil || !found {
		e.t.Fatalf("job %d: %v, found %v", id, err, found)
	}
	return job
}

func (e *execTestEnv) jobPath(job execution.Job, suffix string) string {
	return fmt.Sprintf("/jobs/%d%s", job.ID, suffix)
}

func execJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- the common rules ---

func TestNewExecutorAPIRefusesAMissingDependency(t *testing.T) {
	artifacts, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	whole := func() executorDeps {
		return executorDeps{token: []byte(execTestToken), store: executiontest.NewMemory(commandstest.NewMemory(ownertest.NewMemory(), githubsynctest.NewMemory())),
			liveness: execution.NewLiveness(execOrigin), times: config.ExecutorTimes{JobTimeout: time.Hour, LeaseWait: time.Hour, GitHubWait: time.Hour}, content: artifacts,
			owners: ownertest.NewMemory(), trees: (&fakeTrees{}).read, archives: (&fakeArchives{}).read, wake: (&wakeLog{}).wake, stopping: func() bool { return false },
			logger: quietLogger(), now: time.Now}
	}
	if _, err := newExecutorAPI(whole()); err != nil {
		t.Fatalf("a whole composition is refused: %v", err)
	}
	for name, mutate := range map[string]func(*executorDeps){
		"token":    func(d *executorDeps) { d.token = nil },
		"store":    func(d *executorDeps) { d.store = nil },
		"liveness": func(d *executorDeps) { d.liveness = nil },
		"times":    func(d *executorDeps) { d.times = config.ExecutorTimes{} },
		"content":  func(d *executorDeps) { d.content = nil },
		"owners":   func(d *executorDeps) { d.owners = nil },
		"trees":    func(d *executorDeps) { d.trees = nil },
		"archives": func(d *executorDeps) { d.archives = nil },
		"wake":     func(d *executorDeps) { d.wake = nil },
		"stopping": func(d *executorDeps) { d.stopping = nil },
		"logger":   func(d *executorDeps) { d.logger = nil },
		"now":      func(d *executorDeps) { d.now = nil },
	} {
		deps := whole()
		mutate(&deps)
		if handler, err := newExecutorAPI(deps); err == nil || handler != nil {
			t.Errorf("a composition without its %s = %v, %v; want an error and no handler", name, handler, err)
		}
	}
}

// Without a token the routes do not exist: 404 with an empty body, as for a path that was never served.
func TestExecutorRoutesAreNotServedWithoutAToken(t *testing.T) {
	server := httptest.NewServer(newRouter(&api{logger: quietLogger()}))
	t.Cleanup(server.Close)
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/executor/v1/lease"}, {http.MethodPost, "/executor/v1/jobs/1/heartbeat"}, {http.MethodGet, "/executor/v1/jobs/1/archive/head"}, {http.MethodGet, "/executor/v1/nothing"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), call.method, server.URL+call.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+execTestToken) // even a request that carries a token finds nothing
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || len(body) != 0 {
			t.Errorf("%s %s = %d %q; want 404 with an empty body", call.method, call.path, resp.StatusCode, body)
		}
	}
}

func TestExecutorRejectsAMissingOrWrongToken(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	for name, mod := range map[string]execMod{
		"no header":        withoutAuthorization(),
		"another scheme":   withHeader("Authorization", "Basic "+execTestToken),
		"the bare token":   withHeader("Authorization", execTestToken),
		"empty bearer":     withHeader("Authorization", "Bearer "),
		"a wrong token":    withToken("wrong-token-0123456789-abcdefghijklmnop"),
		"a longer token":   withToken(execTestToken + "x"),
		"a shorter token":  withToken(execTestToken[:len(execTestToken)-1]),
		"the token cased":  withToken(strings.ToUpper(execTestToken)),
		"a token in other": withHeader("X-Token", execTestToken),
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/lease"}, {http.MethodPost, "/jobs/1/heartbeat"}, {http.MethodGet, "/jobs/1/archive/head"}, {http.MethodGet, "/jobs/1/files/" + execDigest("x").String()},
			{http.MethodPost, "/jobs/1/result"}, {http.MethodGet, "/nothing"},
		} {
			got := e.send(route.method, route.path, nil, withoutAuthorization(), mod)
			if got.status != http.StatusUnauthorized || len(got.body) != 0 {
				t.Errorf("%s: %s %s = %d %q; want 401 with no detail", name, route.method, route.path, got.status, got.body)
			}
		}
	}
	if job := e.job(1); job.State != execution.StatePending || job.Attempts != 0 {
		t.Errorf("job after the refused requests = %+v; want it untouched", job)
	}
	if _, requested := e.liveness.LastRequest(); requested {
		t.Error("a refused request counts as an executor seen")
	}
	if strings.Contains(e.logs.String(), execTestToken) || strings.Contains(e.logs.String(), "wrong-token") {
		t.Errorf("the log holds a token: %s", e.logs.String())
	}
}

func TestExecutorAnswersAreNeverCached(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for name, got := range map[string]execReply{
		"204":       e.send(http.MethodPost, "/lease", nil),
		"401":       e.send(http.MethodPost, "/lease", nil, withToken("nope")),
		"404":       e.send(http.MethodGet, "/nothing", nil),
		"heartbeat": e.send(http.MethodPost, e.jobPath(job, "/heartbeat"), nil, withLease(leased.LeaseID)),
		"409":       e.send(http.MethodPost, e.jobPath(job, "/heartbeat"), nil, withLease(execSHA(9)[:32])),
	} {
		if got.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q; want no-store", name, got.header.Get("Cache-Control"))
		}
	}
}

func TestExecutorAnswersUnknownRoutesWithAFixedCode(t *testing.T) {
	e := newExecEnv(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/nothing"}, {http.MethodGet, "/lease"}, {http.MethodPut, "/lease"}, {http.MethodPost, "/jobs/1"}, {http.MethodPost, "/jobs/1/archive/head"},
		{http.MethodGet, "/jobs/1/heartbeat"}, {http.MethodPost, "/jobs/1/files/sha256:x"}, {http.MethodGet, "/jobs/1/result"}, {http.MethodDelete, "/jobs/1/result"},
	} {
		e.wantError(e.send(route.method, route.path, nil), http.StatusNotFound, "not-found")
	}
}

func TestExecutorIsUnavailableWhileTheServiceStops(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	e.stopping.Store(true)
	e.wantError(e.send(http.MethodPost, "/lease", nil), http.StatusServiceUnavailable, "unavailable")
	e.wantError(e.send(http.MethodPost, "/jobs/1/heartbeat", nil), http.StatusServiceUnavailable, "unavailable")
	if got := e.send(http.MethodPost, "/lease", nil, withToken("nope")); got.status != http.StatusUnauthorized {
		t.Errorf("a wrong token while the service stops = %d; want 401, the token comes first", got.status)
	}
	if job := e.job(1); job.State != execution.StatePending {
		t.Errorf("job = %+v; want it pending, nothing was leased while stopping", job)
	}
}
