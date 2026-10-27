package run

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// The harness of the executor's tests: a fake of the service (the executor API of docs/contracts/runtime.md, served over HTTP to the real
// client), a fake of the container runtime that reads the standard input to its end the way a container does, and the repositories
// that both of them serve as tar.gz archives in GitHub's layout.

const (
	testToken = "an-executor-token-of-more-than-32-bytes"
	headSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	modPath   = "example.com/m"
	goMod     = "module " + modPath + "\n\ngo 1.21\n"
	// declaration approves every test file and names the Go profile.
	declaration = "version: 1\ninclude:\n  - \"**/*_test.go\"\nexecution:\n  profile: go\n"
)

var testBinary = strings.Repeat("ab", 32)

func defaultLimits() client.Limits {
	return client.Limits{
		ArchiveBytes: 256 << 20, FileBytes: 8 << 20, TestTimeoutSeconds: 600, MemoryMiB: 3072, CPUs: 4, Pids: 512, TmpfsMiB: 1024,
		PrefetchModules: 2000, PrefetchBytes: 512 << 20, Reruns: 2,
	}
}

// repo is the files of one commit, or of one manifest, by path.
type repo map[string]string

func (r repo) paths() []string { return slices.Sorted(maps.Keys(r)) }

// entries are the entries of the commit's tree; extra adds the ones that no archive holds (a submodule).
func (r repo) entries(extra ...client.TreeEntry) []client.TreeEntry {
	var out []client.TreeEntry
	for _, p := range r.paths() {
		out = append(out, client.TreeEntry{Path: p, Mode: "100644", SHA: execution.BlobSHA1([]byte(r[p]))})
	}
	return append(out, extra...)
}

// archive is the tar.gz that GitHub makes of the commit: a pax global header and one top-level directory.
func (r repo) archive(sha string) []byte {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": sha}}))
	top := "owner-repo-" + sha[:7]
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top + "/", Mode: 0o755}))
	for _, p := range r.paths() {
		must(tw.WriteHeader(&tar.Header{Name: top + "/" + p, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(r[p]))}))
		_, err := tw.Write([]byte(r[p]))
		must(err)
	}
	must(tw.Close())
	must(gz.Close())
	return raw.Bytes()
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// manifestOf is the manifest that names the files of m, and their bytes by digest.
func manifestOf(m repo) (client.Manifest, map[string][]byte) {
	var entries []artifact.Entry
	var files []client.ManifestFile
	contents := map[string][]byte{}
	for _, p := range m.paths() {
		d := artifact.Hash([]byte(m[p]))
		entries = append(entries, artifact.Entry{Path: p, Content: d})
		files = append(files, client.ManifestFile{Path: p, Digest: d.String()})
		contents[d.String()] = []byte(m[p])
	}
	manifest, err := artifact.NewManifest(entries)
	must(err)
	return client.Manifest{Source: "V1", Digest: manifest.Digest().String(), Files: files}, contents
}

// scenario is a job and what the service serves for it.
type scenario struct {
	plan                 string
	head, base           repo
	headExtra, baseExtra []client.TreeEntry
	// manifest is the governing manifest of a canonical job, or the canonical version that a receipt's revision expects.
	manifest repo
	limits   client.Limits
	profile  client.Profile
	deadline time.Time
}

func canonicalScenario() *scenario {
	return &scenario{
		plan: client.PlanCanonical, head: repo{"go.mod": goMod}, manifest: repo{".suiteward.yml": declaration},
		limits: defaultLimits(), profile: client.Profile{ID: "go", Image: goprofile.Image}, deadline: time.Now().Add(time.Hour),
	}
}

func receiptScenario() *scenario {
	s := canonicalScenario()
	s.plan = client.PlanReceipt
	s.base = repo{"go.mod": goMod}
	return s
}

func (s *scenario) job(id int64) client.Job {
	manifest, _ := manifestOf(s.manifest)
	job := client.Job{
		JobID: id, LeaseID: fmt.Sprintf("%032x", id), LeaseExpiresAt: time.Now().Add(time.Minute), DeadlineAt: s.deadline, Plan: s.plan, Pull: 12,
		Profile: s.profile, Head: client.Commit{SHA: headSHA, Tree: s.head.entries(s.headExtra...)}, Manifest: manifest, Limits: s.limits,
	}
	if s.plan == client.PlanReceipt {
		job.Base = &client.Commit{SHA: baseSHA, Tree: s.base.entries(s.baseExtra...)}
	}
	if d, ok := s.manifest[".suiteward.yml"]; ok {
		job.Declaration = []byte(d)
	}
	return job
}

// served is a job that the service grants, and how it answers the calls under it.
type served struct {
	job      client.Job
	archives map[string]func(http.ResponseWriter)
	files    map[string][]byte
	beat     func(n int) int           // the status of the n-th heartbeat, from 1
	answer   func(n int) (int, string) // the status and the state of the n-th result
	beats    atomic.Int32
	results  atomic.Int32
}

func (s *scenario) serve(id int64) *served {
	_, contents := manifestOf(s.manifest)
	sv := &served{job: s.job(id), archives: map[string]func(http.ResponseWriter){}, files: contents}
	sv.archives["head"] = bytesAnswer(s.head.archive(headSHA))
	if s.plan == client.PlanReceipt {
		sv.archives["base"] = bytesAnswer(s.base.archive(baseSHA))
	}
	return sv
}

func bytesAnswer(b []byte) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(b)
	}
}

func statusAnswer(status int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(status) }
}

type posted struct {
	id    int64
	lease string
	body  []byte
}

// service is the fake of the executor API. Its lease answers follow the steps that a test queues: a *served grants the job, an int is
// the status of an error, and an empty queue is a 204.
type service struct {
	*httptest.Server
	mu     sync.Mutex
	steps  []any
	leases int
	jobs   map[int64]*served
	posts  chan posted
}

func newService(t *testing.T, steps ...any) *service {
	t.Helper()
	s := &service{steps: steps, jobs: map[int64]*served{}, posts: make(chan posted, 64)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /executor/v1/lease", s.lease)
	mux.HandleFunc("POST /executor/v1/jobs/{id}/heartbeat", s.job(s.heartbeat))
	mux.HandleFunc("POST /executor/v1/jobs/{id}/result", s.job(s.result))
	mux.HandleFunc("GET /executor/v1/jobs/{id}/archive/{side}", s.job(s.archive))
	mux.HandleFunc("GET /executor/v1/jobs/{id}/files/{digest}", s.job(s.file))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *service) leaseRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leases
}

func (s *service) lease(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.leases++
	var step any
	if len(s.steps) > 0 {
		step, s.steps = s.steps[0], s.steps[1:]
	}
	s.mu.Unlock()
	switch step := step.(type) {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case int:
		w.WriteHeader(step)
	case *served:
		s.mu.Lock()
		s.jobs[step.job.JobID] = step
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		must(json.NewEncoder(w).Encode(step.job))
	}
}

// job wraps a handler of a call under a job: the lease header must be the job's lease, or the answer is the 409 of a lost lease.
func (s *service) job(next func(w http.ResponseWriter, r *http.Request, sv *served)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s.mu.Lock()
		sv := s.jobs[id]
		s.mu.Unlock()
		switch {
		case sv == nil:
			w.WriteHeader(http.StatusNotFound)
		case r.Header.Get("Suiteward-Lease") != sv.job.LeaseID:
			w.WriteHeader(http.StatusConflict)
		default:
			next(w, r, sv)
		}
	}
}

func (s *service) heartbeat(w http.ResponseWriter, _ *http.Request, sv *served) {
	n := int(sv.beats.Add(1))
	status := http.StatusOK
	if sv.beat != nil {
		status = sv.beat(n)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"leaseExpiresAt":"2026-10-27T10:00:00Z"}`)
}

func (s *service) result(w http.ResponseWriter, r *http.Request, sv *served) {
	body, _ := io.ReadAll(r.Body)
	n := int(sv.results.Add(1))
	s.posts <- posted{id: sv.job.JobID, lease: r.Header.Get("Suiteward-Lease"), body: body}
	status, state := http.StatusOK, ""
	if sv.answer != nil {
		status, state = sv.answer(n)
	}
	if state == "" {
		var b struct{ Status string }
		_ = json.Unmarshal(body, &b)
		state = b.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"state":%q}`, state)
}

func (s *service) archive(w http.ResponseWriter, r *http.Request, sv *served) {
	if serve, ok := sv.archives[r.PathValue("side")]; ok {
		serve(w)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *service) file(w http.ResponseWriter, r *http.Request, sv *served) {
	if content, ok := sv.files[r.PathValue("digest")]; ok {
		_, _ = w.Write(content)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// call is a go test invocation as the fake runtime saw it: what the sandbox would have extracted, and what argv names.
type call struct {
	Spec        sandbox.Spec
	Entries     []string          // the names in the tar, in order
	Files       map[string][]byte // the regular files by name
	Names, Dirs []string          // the tests that -run selects and the packages of the invocation (. for the root)
}

// fakeRuntime is a sandbox.Runtime that reads the standard input to its end and checks its tar as the container's script does.
type fakeRuntime struct {
	mu        sync.Mutex
	infoErr   error
	goVersion string
	probeErr  error
	runs      []call
	probes    int
	skipStdin bool          // fail at once, without reading the standard input
	gate      chan struct{} // when set, an invocation of go test waits for a receive, or for its context
	entered   chan struct{} // when set, an invocation tells that it is running before it waits
	// test answers an invocation of go test.
	test func(c call) (sandbox.Outcome, error)
}

func newRuntime(test func(c call) (sandbox.Outcome, error)) *fakeRuntime {
	return &fakeRuntime{goVersion: "go1.27.3", test: test}
}

func (f *fakeRuntime) Info(context.Context) (string, string, error) {
	return "docker", "29.1.0", f.infoErr
}

func (f *fakeRuntime) invocations() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

func (f *fakeRuntime) Run(ctx context.Context, spec sandbox.Spec) (sandbox.Outcome, error) {
	probe := slices.Equal(spec.Argv, []string{"go", "env", "GOVERSION"})
	if f.skipStdin && !probe {
		return sandbox.Outcome{}, fmt.Errorf("%w: the fake runtime failed at once", sandbox.ErrUnavailable)
	}
	c := call{Spec: spec, Files: map[string][]byte{}}
	tr := tar.NewReader(spec.Stdin)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sandbox.Outcome{}, fmt.Errorf("read the standard input of the sandbox: %w", err)
		}
		c.Entries = append(c.Entries, h.Name)
		if h.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return sandbox.Outcome{}, fmt.Errorf("read the standard input of the sandbox: %w", err)
			}
			c.Files[h.Name] = data
		}
	}
	if len(c.Entries) == 0 || c.Entries[len(c.Entries)-1] != sandbox.CompleteMarker {
		return sandbox.Outcome{}, fmt.Errorf("%w: the tar does not end with the completion marker", sandbox.ErrSetupFailed)
	}
	if probe {
		f.mu.Lock()
		f.probes++
		f.mu.Unlock()
		if f.probeErr != nil {
			return sandbox.Outcome{}, f.probeErr
		}
		return sandbox.Outcome{Stdout: []byte(f.goVersion + "\n")}, nil
	}
	c.Names, c.Dirs = selection(spec.Argv)
	f.mu.Lock()
	f.runs = append(f.runs, c)
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return sandbox.Outcome{}, ctx.Err()
		}
	}
	return f.test(c)
}

// selection reads the tests and the packages that the argv of one go test invocation names.
func selection(argv []string) (names, dirs []string) {
	i := slices.Index(argv, "-run")
	if i < 0 || i+1 >= len(argv) {
		return nil, nil
	}
	pattern := strings.TrimSuffix(strings.TrimPrefix(argv[i+1], "^("), ")$")
	names = strings.Split(pattern, "|")
	for _, dir := range argv[i+2:] {
		dirs = append(dirs, strings.TrimPrefix(dir, "./"))
	}
	return names, dirs
}

var moduleLine = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// moduleOf is the module path of a go.mod, which names the packages that go test reports.
func moduleOf(goMod []byte) string {
	if m := moduleLine.FindSubmatch(goMod); m != nil {
		return string(m[1])
	}
	return ""
}

// goTest answers an invocation as go test -json does: each selected test of each selected package gets the result that results names
// ("pass", "fail" or "skip"; "" for a test that reports nothing, and "build-error" for a package that does not build).
func goTest(c call, results func(dir, name string) string) sandbox.Outcome {
	var out bytes.Buffer
	exit := 0
	event := func(action, pkg, test, failedBuild string) {
		line := map[string]string{"Action": action, "Package": pkg}
		if test != "" {
			line["Test"] = test
		}
		if failedBuild != "" {
			line["FailedBuild"] = failedBuild
		}
		b, err := json.Marshal(line)
		must(err)
		out.Write(append(b, '\n'))
	}
	module := moduleOf(c.Files["src/go.mod"])
	for _, dir := range c.Dirs {
		pkg := module
		if dir != "." {
			pkg += "/" + dir
		}
		failed, built := false, true
		for _, name := range c.Names {
			switch results(dir, name) {
			case "build-error":
				built = false
			case "pass":
				event("run", pkg, name, "")
				event("pass", pkg, name, "")
			case "skip":
				event("run", pkg, name, "")
				event("skip", pkg, name, "")
			case "fail":
				event("run", pkg, name, "")
				event("fail", pkg, name, "")
				failed = true
			}
		}
		switch {
		case !built:
			event("fail", pkg, "", pkg+" [build failed]")
			exit = 1
		case failed:
			event("fail", pkg, "", "")
			exit = 1
		default:
			event("pass", pkg, "", "")
		}
	}
	return sandbox.Outcome{ExitCode: exit, Stdout: out.Bytes()}
}

// sleeper is the Sleep of an executor under test: the first immediate waits return at once, and the others wait for the context.
type sleeper struct {
	mu        sync.Mutex
	immediate int
	waited    []time.Duration
	reached   chan struct{} // closed when the waits that return at once are all used up
	once      sync.Once
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waited = append(s.waited, d)
	n := len(s.waited)
	s.mu.Unlock()
	if n <= s.immediate {
		return ctx.Err()
	}
	if s.reached != nil {
		s.once.Do(func() { close(s.reached) })
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *sleeper) durations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.waited)
}

// clock is the Now of an executor under test: it starts at a fixed time and moves one second at each reading.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

// driver runs an executor against a fake service and a fake runtime.
type driver struct {
	t        *testing.T
	svc      *service
	patience time.Duration // how long a test waits for the executor before it gives up
	sleeps   *sleeper
	ticks    chan time.Time
	tickFor  chan time.Duration
	swept    atomic.Int32
	cancel   context.CancelFunc
	done     chan error
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// start builds the executor with the settings of a test, runs it, and stops it when the test ends.
func start(t *testing.T, svc *service, rt sandbox.Runtime, options ...func(*Config, *driver)) *driver {
	t.Helper()
	api, err := client.New(svc.URL, config.Secret(testToken))
	if err != nil {
		t.Fatal(err)
	}
	d := &driver{t: t, svc: svc, patience: 30 * time.Second, sleeps: &sleeper{}, ticks: make(chan time.Time), tickFor: make(chan time.Duration, 16), done: make(chan error, 1)}
	cfg := Config{
		API: api, Runtime: rt, Version: "1.2.3", BinarySHA256: testBinary,
		Sweep: func(context.Context) error { d.swept.Add(1); return nil },
		Log:   slog.New(slog.NewTextHandler(testLog{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sleep: d.sleeps.sleep,
		Tick: func(every time.Duration) (<-chan time.Time, func()) {
			d.tickFor <- every
			return d.ticks, func() {}
		},
		Now: (&clock{t: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}).now,
	}
	for _, option := range options {
		option(&cfg, d)
	}
	ex, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() { d.done <- ex.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.done:
		case <-time.After(d.patience):
			t.Error("the executor did not stop")
		}
	})
	return d
}

// stopped waits for Run to return, and gives what it returned.
func (d *driver) stopped() error {
	d.t.Helper()
	select {
	case err := <-d.done:
		d.done <- err
		return err
	case <-time.After(d.patience):
		d.t.Fatal("the executor did not stop")
		return nil
	}
}

// result waits for the next result that the service receives.
func (d *driver) result() posted {
	d.t.Helper()
	select {
	case p := <-d.svc.posts:
		return p
	case err := <-d.done:
		d.done <- err
		d.t.Fatalf("the executor stopped (%v) before it posted a result", err)
	case <-time.After(d.patience):
		d.t.Fatal("no result was posted")
	}
	return posted{}
}

// noResult fails when a result is posted: it is meant to be called when the work is over.
func (d *driver) noResult() {
	d.t.Helper()
	select {
	case p := <-d.svc.posts:
		d.t.Fatalf("a result was posted: %s", p.body)
	default:
	}
}

// decode reads a posted body as the generic JSON that the service reads.
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("the body is not JSON: %v: %s", err, body)
	}
	return m
}

var rfc3339 = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

// assertBody compares a posted body with the JSON that the contract defines, key by key, leaving the two times: they are read apart.
func assertBody(t *testing.T, body []byte, want string) map[string]any {
	t.Helper()
	got := decode(t, body)
	started, ended := got["startedAt"], got["endedAt"]
	for name, v := range map[string]any{"startedAt": started, "endedAt": ended} {
		s, _ := v.(string)
		if !rfc3339.MatchString(s) {
			t.Errorf("%s is %v; want RFC 3339 in UTC", name, v)
		}
	}
	if s, e := started.(string), ended.(string); e < s {
		t.Errorf("the run ended (%s) before it started (%s)", e, s)
	}
	delete(got, "startedAt")
	delete(got, "endedAt")
	var wantMap map[string]any
	if err := json.Unmarshal([]byte(want), &wantMap); err != nil {
		t.Fatalf("the expected body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(got, wantMap) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(wantMap, "", "  ")
		t.Errorf("the body is\n%s\nwant\n%s", g, w)
	}
	return got
}

// The fields of a complete result that do not change between the tests.
const (
	commonExecutor = `"executor": {"version": "1.2.3", "binarySha256": "abababababababababababababababababababababababababababababababab"}`
	commonSandbox  = `"sandbox": {"imageDigest": "sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd", "toolchain": "go1.27.3", "runtime": "docker 29.1.0"}`
)

// moduleZip is a module zip with one file of n bytes, as a proxy serves it, and its .mod file.
func moduleZip(t *testing.T, path, version string, n int) (zipFile, modFile []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(path + "@" + version + "/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte{'z'}, n)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), []byte("module " + path + "\n")
}

// proxyServing is a module proxy of the files by name, with a client that trusts it.
func proxyServing(t *testing.T, files map[string][]byte) Proxy {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return Proxy{Base: srv.URL, Client: srv.Client()}
}

func (f *fakeRuntime) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

// wait receives from c, and fails the test when the executor stops, or a long time passes, before that.
func wait[T any](d *driver, c <-chan T) T {
	d.t.Helper()
	select {
	case v := <-c:
		return v
	case err := <-d.done:
		d.done <- err
		d.t.Fatalf("the executor stopped (%v) while the test waited for it", err)
	case <-time.After(d.patience):
		d.t.Fatal("the test waited for the executor in vain")
	}
	var zero T
	return zero
}

// tick makes the heartbeat's ticker fire once.
func (d *driver) tick() {
	d.t.Helper()
	select {
	case d.ticks <- time.Now():
	case err := <-d.done:
		d.done <- err
		d.t.Fatalf("the executor stopped (%v) before the heartbeat ticked", err)
	case <-time.After(d.patience):
		d.t.Fatal("nothing took the tick")
	}
}
