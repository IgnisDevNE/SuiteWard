package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const (
	token   = "0123456789abcdef0123456789abcdef-test-token"
	leaseID = "0123456789abcdef0123456789abcdef"
	headSHA = "1111111111111111111111111111111111111111"
	baseSHA = "2222222222222222222222222222222222222222"
	digest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	image   = "registry.example.com/images/toolchain:1@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// leaseAnswer is a valid answer of POST /lease for a receipt, as a map so that a test changes one field.
func leaseAnswer() map[string]any {
	return map[string]any{
		"jobId": 7, "leaseId": leaseID, "leaseExpiresAt": "2026-10-27T10:01:00Z", "deadlineAt": "2026-10-27T10:30:00Z",
		"plan": "receipt", "pull": 12,
		"profile": map[string]any{"id": "go", "image": image},
		"head":    map[string]any{"sha": headSHA, "tree": []any{map[string]any{"path": "go.mod", "mode": "100644", "sha": headSHA}}},
		"base":    map[string]any{"sha": baseSHA, "tree": []any{}},
		"manifest": map[string]any{
			"source": "V1", "digest": digest,
			"files": []any{map[string]any{"path": ".suiteward.yml", "digest": digest}},
		},
		"declaration": base64.StdEncoding.EncodeToString([]byte("version: 1\n")),
		"limits": map[string]any{
			"archiveBytes": 268435456, "fileBytes": 8388608, "testTimeoutSeconds": 600, "memoryMiB": 3072, "cpus": 4, "pids": 512,
			"tmpfsMiB": 1024, "prefetchModules": 2000, "prefetchBytes": 536870912, "reruns": 2,
		},
	}
}

// served is a service that records the requests it gets and answers with handler.
type served struct {
	*httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func serve(t *testing.T, handler http.HandlerFunc) *served {
	t.Helper()
	s := &served{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r)
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *served) requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.seen...)
}

func newClient(t *testing.T, s *served) *Client {
	t.Helper()
	c, err := New(s.URL, config.Secret(token))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// leased returns a lease that the service granted with the answer.
func leased(t *testing.T, answer map[string]any, handler http.HandlerFunc) (*Lease, *served) {
	t.Helper()
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/executor/v1/lease" {
			writeJSON(w, http.StatusOK, answer)
			return
		}
		handler(w, r)
	})
	lease, err := newClient(t, s).Lease(context.Background())
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	return lease, s
}

func TestLeaseReadsTheJob(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, leaseAnswer()) })
	lease, err := newClient(t, s).Lease(context.Background())
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	want := Job{
		JobID: 7, LeaseID: leaseID, Plan: PlanReceipt, Pull: 12,
		LeaseExpiresAt: time.Date(2026, 10, 27, 10, 1, 0, 0, time.UTC), DeadlineAt: time.Date(2026, 10, 27, 10, 30, 0, 0, time.UTC),
		Profile:     Profile{ID: "go", Image: image},
		Head:        Commit{SHA: headSHA, Tree: []TreeEntry{{Path: "go.mod", Mode: "100644", SHA: headSHA}}},
		Base:        &Commit{SHA: baseSHA, Tree: []TreeEntry{}},
		Manifest:    Manifest{Source: "V1", Digest: digest, Files: []ManifestFile{{Path: ".suiteward.yml", Digest: digest}}},
		Declaration: []byte("version: 1\n"),
		Limits: Limits{
			ArchiveBytes: 268435456, FileBytes: 8388608, TestTimeoutSeconds: 600, MemoryMiB: 3072, CPUs: 4, Pids: 512, TmpfsMiB: 1024,
			PrefetchModules: 2000, PrefetchBytes: 536870912, Reruns: 2,
		},
	}
	if !reflect.DeepEqual(lease.Job, want) {
		t.Errorf("the job is %+v; want %+v", lease.Job, want)
	}
	got := s.requests()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].URL.Path != "/executor/v1/lease" {
		t.Fatalf("the requests were %v", got)
	}
	if got[0].Header.Get("Authorization") != "Bearer "+token || got[0].Header.Get("Suiteward-Lease") != "" || got[0].ContentLength > 0 {
		t.Errorf("the lease request carries %v with a body of %d bytes", got[0].Header, got[0].ContentLength)
	}
}

func TestLeaseAnswersByStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusNoContent, ErrNoJob},
		{http.StatusBadGateway, ErrGitHub},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusInternalServerError, ErrServer},
		{http.StatusNotFound, ErrNotFound}, // the executor routes are not served without a token on the service
		{http.StatusBadRequest, ErrInvalid},
	} {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.status == http.StatusNoContent {
				w.WriteHeader(tc.status)
				return
			}
			writeJSON(w, tc.status, map[string]string{"error": "x"})
		})
		if _, err := newClient(t, s).Lease(context.Background()); !errors.Is(err, tc.want) {
			t.Errorf("a lease answered %d gave %v; want %v", tc.status, err, tc.want)
		}
	}
}

func TestLeaseRefusesAnAnswerThatBreaksTheContract(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"no job id":                    func(a map[string]any) { a["jobId"] = 0 },
		"a lease id of upper case":     func(a map[string]any) { a["leaseId"] = strings.ToUpper(leaseID) },
		"a short lease id":             func(a map[string]any) { a["leaseId"] = "abc" },
		"an unknown plan":              func(a map[string]any) { a["plan"] = "other" },
		"a receipt without a base":     func(a map[string]any) { delete(a, "base") },
		"a canonical with a base":      func(a map[string]any) { a["plan"] = "canonical" },
		"a head that is not a SHA":     func(a map[string]any) { a["head"].(map[string]any)["sha"] = "abc" },
		"a base that is not a SHA":     func(a map[string]any) { a["base"].(map[string]any)["sha"] = "ABCDEF1111111111111111111111111111111111" },
		"no profile":                   func(a map[string]any) { a["profile"] = map[string]any{} },
		"an image that is not pinned":  func(a map[string]any) { a["profile"].(map[string]any)["image"] = "golang:latest" },
		"a manifest source of no kind": func(a map[string]any) { a["manifest"].(map[string]any)["source"] = "main" },
		"a manifest digest of no kind": func(a map[string]any) { a["manifest"].(map[string]any)["digest"] = "abc" },
		"a manifest file digest of no kind": func(a map[string]any) {
			a["manifest"].(map[string]any)["files"] = []any{map[string]any{"path": "a", "digest": "abc"}}
		},
		"no deadline":              func(a map[string]any) { delete(a, "deadlineAt") },
		"a limit of zero":          func(a map[string]any) { a["limits"].(map[string]any)["cpus"] = 0 },
		"a negative limit":         func(a map[string]any) { a["limits"].(map[string]any)["fileBytes"] = -1 },
		"a negative rerun count":   func(a map[string]any) { a["limits"].(map[string]any)["reruns"] = -1 },
		"a tmpfs over the memory":  func(a map[string]any) { a["limits"].(map[string]any)["tmpfsMiB"] = 4096 },
		"a limit past any machine": func(a map[string]any) { a["limits"].(map[string]any)["memoryMiB"] = int64(1) << 60 },
	} {
		answer := leaseAnswer()
		change(answer)
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, answer) })
		if _, err := newClient(t, s).Lease(context.Background()); !errors.Is(err, ErrBadAnswer) {
			t.Errorf("%s: Lease gave %v; want ErrBadAnswer", name, err)
		}
	}
}

func TestLeaseTakesACanonicalJobWithNoDeclarationAndIgnoresNewFields(t *testing.T) {
	answer := leaseAnswer()
	answer["plan"] = "canonical"
	delete(answer, "base")
	delete(answer, "declaration")
	answer["somethingNew"] = true
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, answer) })
	lease, err := newClient(t, s).Lease(context.Background())
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if lease.Plan != PlanCanonical || lease.Base != nil || len(lease.Declaration) != 0 {
		t.Errorf("the job is %+v", lease.Job)
	}
}

func TestLeaseRefusesAnAnswerOverSixteenMiB(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"pad":"`)
		_, _ = io.CopyN(w, zeroes{}, 17<<20)
		_, _ = io.WriteString(w, `"}`)
	})
	if _, err := newClient(t, s).Lease(context.Background()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Lease gave %v; want ErrTooLarge", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestEveryCallUnderAJobCarriesTheLease(t *testing.T) {
	lease, s := leased(t, leaseAnswer(), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			writeJSON(w, http.StatusOK, map[string]string{"leaseExpiresAt": "2026-10-27T10:02:00Z"})
		case strings.HasSuffix(r.URL.Path, "/result"):
			writeJSON(w, http.StatusOK, map[string]string{"state": "complete"})
		default:
			_, _ = io.WriteString(w, "bytes")
		}
	})
	ctx := context.Background()
	if err := lease.Heartbeat(ctx); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}
	if state, err := lease.Result(ctx, []byte(`{"status":"complete"}`)); err != nil || state != StateComplete {
		t.Errorf("Result = %q, %v", state, err)
	}
	for _, open := range []func() (io.ReadCloser, error){
		func() (io.ReadCloser, error) { return lease.Archive(ctx, "head") },
		func() (io.ReadCloser, error) { return lease.Archive(ctx, "base") },
		func() (io.ReadCloser, error) { return lease.File(ctx, digest) },
	} {
		rc, err := open()
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if body, _ := io.ReadAll(rc); string(body) != "bytes" {
			t.Errorf("the body is %q", body)
		}
		_ = rc.Close()
	}
	want := []string{
		"POST /executor/v1/jobs/7/heartbeat", "POST /executor/v1/jobs/7/result",
		"GET /executor/v1/jobs/7/archive/head", "GET /executor/v1/jobs/7/archive/base", "GET /executor/v1/jobs/7/files/" + digest,
	}
	got := s.requests()[1:]
	if len(got) != len(want) {
		t.Fatalf("the requests after the lease were %d, want %d", len(got), len(want))
	}
	for i, r := range got {
		if r.Method+" "+r.URL.Path != want[i] || r.Header.Get("Suiteward-Lease") != leaseID || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("request %d was %s %s with %v; want %s with the lease and the token", i, r.Method, r.URL.Path, r.Header, want[i])
		}
	}
}

func TestResultSendsTheBodyOnceAndReadsTheState(t *testing.T) {
	for _, state := range []string{StateComplete, StateUnavailable, StatePending} {
		var gotBody string
		var gotType string
		lease, _ := leased(t, leaseAnswer(), func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody, gotType = string(b), r.Header.Get("Content-Type")
			writeJSON(w, http.StatusOK, map[string]string{"state": state})
		})
		got, err := lease.Result(context.Background(), []byte(`{"status":"x"}`))
		if err != nil || got != state {
			t.Errorf("Result = %q, %v; want %q", got, err, state)
		}
		if gotBody != `{"status":"x"}` || gotType != "application/json" {
			t.Errorf("the service got %q as %q", gotBody, gotType)
		}
	}
}

func TestResultRefusesAStateThatIsNoneOfThree(t *testing.T) {
	lease, _ := leased(t, leaseAnswer(), func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"state": "cancelled"})
	})
	if _, err := lease.Result(context.Background(), []byte(`{}`)); !errors.Is(err, ErrBadAnswer) {
		t.Errorf("Result gave %v; want ErrBadAnswer", err)
	}
}

func TestCallsUnderAJobAnswerByStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusConflict, ErrLeaseLost},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusBadRequest, ErrInvalid},
		{http.StatusRequestEntityTooLarge, ErrTooLarge},
		{http.StatusBadGateway, ErrGitHub},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusInternalServerError, ErrServer},
	} {
		lease, _ := leased(t, leaseAnswer(), func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, tc.status, map[string]string{"error": "x"})
		})
		ctx := context.Background()
		_, resultErr := lease.Result(ctx, []byte(`{}`))
		calls := map[string]error{
			"heartbeat": lease.Heartbeat(ctx),
			"result":    resultErr,
			"archive":   openErr(lease.Archive(ctx, "head")),
			"file":      openErr(lease.File(ctx, digest)),
		}
		for name, err := range calls {
			if !errors.Is(err, tc.want) {
				t.Errorf("%s answered %d gave %v; want %v", name, tc.status, err, tc.want)
			}
		}
	}
}

func openErr(rc io.ReadCloser, err error) error {
	if rc != nil {
		_ = rc.Close()
	}
	return err
}

func TestAnErrorNeverCarriesTheTokenOrTheBodyOfTheAnswer(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "echo " + r.Header.Get("Authorization") + " secret-detail"})
	})
	_, err := newClient(t, s).Lease(context.Background())
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "secret-detail") {
		t.Errorf("the error is %v: it must name neither the token nor what the service echoed", err)
	}
}

func TestADownloadThatStallsIsCutAtItsTimeout(t *testing.T) {
	lease, _ := leased(t, leaseAnswer(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	lease.c.downloadTimeout = 50 * time.Millisecond
	rc, err := lease.Archive(context.Background(), "head")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.ReadAll(rc); err == nil {
		t.Error("a stream that stalled read to its end; it must end with an error")
	}
}

func TestAnAPICallThatTheServiceNeverAnswersIsCutAtItsTimeout(t *testing.T) {
	block := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { <-block })
	defer close(block)
	c := newClient(t, s)
	c.callTimeout = 50 * time.Millisecond
	if _, err := c.Lease(context.Background()); err == nil {
		t.Error("Lease returned without an answer")
	}
}

func TestACanceledContextEndsACall(t *testing.T) {
	block := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { <-block })
	defer close(block)
	c := newClient(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Lease(ctx); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Lease gave %v; want context.Canceled", err)
	}
}

func TestTheClientNeverFollowsARedirectNorReadsAProxy(t *testing.T) {
	other := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	})
	if _, err := newClient(t, s).Lease(context.Background()); err == nil {
		t.Error("Lease followed a redirect, or took it for a job")
	}
	if got := other.requests(); len(got) != 0 {
		t.Errorf("the token went to another host: %v", got)
	}
	c := newClient(t, s)
	transport, ok := c.hc.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Errorf("the transport is %T with a proxy function %v; the executor goes direct", c.hc.Transport, ok && transport.Proxy != nil)
	}
	// The body of a download streams for as long as the sandbox takes to read it: http.Client.Timeout would cut it.
	if c.hc.Timeout != 0 {
		t.Errorf("the client has the overall timeout %v, which would cut a download", c.hc.Timeout)
	}
}

func TestOriginsThatAreAccepted(t *testing.T) {
	valid := []string{
		"https://suiteward.example.com", "https://suiteward.example.com:8443", "https://suiteward.example.com/",
		"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080", "http://127.0.0.1",
	}
	invalid := []string{
		"", "suiteward.example.com", "ftp://suiteward.example.com", "http://suiteward.example.com", "http://10.0.0.5:8080",
		"https://suiteward.example.com/executor", "https://suiteward.example.com/?x=1", "https://suiteward.example.com#frag",
		"https://user:pw@suiteward.example.com", "https://user@suiteward.example.com", "https:///path", "https://suiteward.example.com?",
		"http://127.0.0.1.example.com:8080", "https://suiteward.example.com//",
	}
	for _, o := range valid {
		if !ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = false; want true", o)
		}
		if _, err := New(o, config.Secret(token)); err != nil {
			t.Errorf("New(%q): %v", o, err)
		}
	}
	for _, o := range invalid {
		if ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = true; want false", o)
		}
		if _, err := New(o, config.Secret(token)); err == nil {
			t.Errorf("New(%q) accepted the origin", o)
		} else if o != "" && strings.Contains(err.Error(), "pw@") {
			t.Errorf("New(%q) echoed the user info: %v", o, err)
		}
	}
	if _, err := New("https://suiteward.example.com", ""); err == nil {
		t.Error("New accepted an empty token")
	}
}
