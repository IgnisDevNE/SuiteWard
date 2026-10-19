//go:build integration

package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

const (
	fakeToken           = "ghs_FAKE-INSTALLATION-TOKEN-VALUE"
	fakeProtectionToken = "ghs_FAKE-PROTECTION-TOKEN-VALUE"
	fakeCommentBody     = "approve this please, comment body GitHub sent"
	fakeErrorBody       = "SECRET-BODY-GITHUB-SENT-WITH-AN-ERROR"
	sha40a              = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha40b              = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGitHub is the GitHub REST API of one installation with one repository, acme/widgets.
type fakeGitHub struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenRequests []map[string]any
	restAuth      []string
	// protectionTokenRequests are the token requests that ask for administration.
	protectionTokenRequests []map[string]any
	// pullsFail, when set, answers the open pull listing with this status, headers and the secret body.
	pullsFail        int
	pullsFailHeaders map[string]string
	// gate, when set before the service starts, holds every open pull listing until a value is sent on it.
	gate chan struct{}
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	if f.gate != nil && r.URL.Path == "/repos/acme/widgets/pulls" {
		select {
		case <-f.gate:
		case <-r.Context().Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		granted, _ := body["permissions"].(map[string]any)
		token := fakeToken
		if _, protection := granted["administration"]; protection {
			f.protectionTokenRequests = append(f.protectionTokenRequests, body)
			token = fakeProtectionToken
		} else {
			f.tokenRequests = append(f.tokenRequests, body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{ // a failed write only fails the test that reads it
			"token": token, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": granted,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets":
		_, _ = w.Write([]byte(`{"id":10,"full_name":"acme/widgets","private":false,"default_branch":"main"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/rules/branches/main":
		_, _ = w.Write([]byte(`[]`))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls":
		f.restAuth = append(f.restAuth, r.Header.Get("Authorization"))
		if f.pullsFail != 0 {
			for name, value := range f.pullsFailHeaders {
				w.Header().Set(name, value)
			}
			w.WriteHeader(f.pullsFail)
			_, _ = w.Write([]byte(fakeErrorBody)) // a failed write only fails the test that reads it
			return
		}
		_, _ = w.Write([]byte(`[{"number":1,"head":{"ref":"feature","sha":"` + sha40a + `"},"base":{"ref":"main","sha":"` + sha40b + `"},"updated_at":"2026-10-10T10:00:00Z"}]`))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/issues/1/comments":
		f.restAuth = append(f.restAuth, r.Header.Get("Authorization"))
		if r.URL.Query().Has("since") { // every cycle lists the comments in full: no local clock decides what to fetch
			http.Error(w, "since is not used", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"id":501,"user":{"login":"octo","id":9},"body":"` + fakeCommentBody + `","created_at":"2026-10-10T10:00:00Z","updated_at":"2026-10-10T10:05:00Z"}]`))
	default:
		http.NotFound(w, r)
	}
}

// githubVars are the settings of a service synchronizing acme/widgets from f, with a generated App key.
func githubVars(t *testing.T, vars map[string]string, f *fakeGitHub) (key string) {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}))
	vars["SUITEWARD_GITHUB_APP_ID"] = "1"
	vars["SUITEWARD_GITHUB_APP_KEY"] = key
	vars["SUITEWARD_GITHUB_INSTALLATION_ID"] = "42"
	vars["SUITEWARD_GITHUB_REPOSITORIES"] = "acme/widgets"
	vars["SUITEWARD_GITHUB_API_URL"] = f.server.URL + "/" // a trailing slash must not double up in the request paths
	return key
}

// waitGitHubStatus waits until the single repository entry of /status satisfies ready and returns it.
func (s *service) waitGitHubStatus(what string, ready func(entry map[string]any) bool) map[string]any {
	s.t.Helper()
	var entry map[string]any
	waitFor(s.t, what, 60*time.Second, func() bool {
		section, _ := s.status()["github"].([]any)
		if len(section) != 1 {
			return false
		}
		entry, _ = section[0].(map[string]any)
		return ready(entry)
	})
	return entry
}

func TestServeSynchronizesGitHubAtStart(t *testing.T) {
	databaseURL := newDatabase(t)
	fake := newFakeGitHub(t)
	vars := serveVars(t, databaseURL)
	key := githubVars(t, vars, fake)
	s := startRun(t, vars)
	s.waitReady()
	// A comment the publisher cannot decode: the relay must still know the kind, so the failure is never "no publisher".
	exec(t, databaseURL, "INSERT INTO outbox (key, kind, payload) VALUES ('composition-probe', 'github_comment', '{}')")

	entry := s.waitGitHubStatus("the first cycle", func(e map[string]any) bool { return e["lastSuccess"] != nil })

	if entry["repo"] != "acme/widgets" || entry["lastError"] != nil || entry["waitUntil"] != nil {
		t.Fatalf("/status github entry = %v; want a success without error or wait", entry)
	}
	for _, kind := range []string{"github_bootstrap", "github_commands", "github_checks", "canonical_integrity"} {
		waitFor(t, kind+" job", 60*time.Second, func() bool {
			return scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = $1", kind) != "0"
		})
	}
	waitFor(t, "the relay to attempt the comment", 60*time.Second, func() bool {
		return scalar(t, databaseURL, "SELECT attempts::text FROM outbox WHERE key = 'composition-probe'") != "0"
	})
	if got := scalar(t, databaseURL, "SELECT coalesce(last_error, '') FROM outbox WHERE key = 'composition-probe'"); strings.Contains(got, "no publisher") {
		t.Fatalf("the relay has no publisher for github_comment: %s", got)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM github_pulls WHERE repo = 'acme/widgets' AND number = 1 AND not_open_since IS NULL"); got != "1" {
		t.Fatalf("%s observed pulls; want the open one", got)
	}
	if got := scalar(t, databaseURL, "SELECT convert_from(body, 'UTF8') FROM github_comment_versions WHERE repo = 'acme/widgets' AND comment_id = 501"); got != fakeCommentBody {
		t.Fatalf("observed comment body = %q; want the one GitHub sent", got)
	}
	fake.mu.Lock()
	tokenRequests, restAuth := fake.tokenRequests, fake.restAuth
	fake.mu.Unlock()
	wantRequest := map[string]any{"repositories": []any{"widgets"}, "permissions": map[string]any{"metadata": "read", "pull_requests": "read", "contents": "read"}}
	if len(tokenRequests) == 0 || !reflect.DeepEqual(tokenRequests[0], wantRequest) {
		t.Fatalf("token requests = %v; want the read-only map for the repository name without its owner: %v", tokenRequests, wantRequest)
	}
	for _, auth := range restAuth {
		if auth != "Bearer "+fakeToken {
			t.Fatalf("a REST request carried %q; want the installation token", auth)
		}
	}
	// The tick is the service's bookkeeping, like the relay: it is not counted.
	zeroJobs := map[string]any{"available": 0.0, "running": 0.0, "retryable": 0.0, "scheduled": 0.0, "completed": 0.0, "discarded": 0.0}
	if jobs := s.status()["jobs"]; !reflect.DeepEqual(jobs, zeroJobs) {
		t.Fatalf("GET /status jobs = %v; want the tick excluded", jobs)
	}

	if code := s.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, s.logs.String())
	}
	for what, secret := range map[string]string{"the installation token": fakeToken, "the App key": strings.TrimSpace(strings.Split(key, "\n")[1]), "a comment body": fakeCommentBody} {
		if strings.Contains(s.logs.String(), secret) {
			t.Errorf("the logs contain %s:\n%s", what, s.logs.String())
		}
	}
}

func TestServeWithoutGitHubSettingsComposesNothingGitHubRelated(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	// The relay's periodic job is inserted at start: once it is there, a tick would have been too.
	waitFor(t, "the periodic jobs to be inserted", 30*time.Second, func() bool {
		return scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = 'outbox_relay'") != "0"
	})

	if section, present := s.status()["github"]; present {
		t.Fatalf("GET /status has a github section without GitHub settings: %v", section)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind LIKE 'github%'"); got != "0" {
		t.Fatalf("%s GitHub jobs without GitHub settings", got)
	}
}

func TestAFailedCycleShowsOnlyItsClassification(t *testing.T) {
	for name, test := range map[string]struct {
		status      int
		headers     map[string]string
		wantError   string
		wantWaiting bool
	}{
		"server error": {status: 500, wantError: "status (HTTP 500)"},
		"unauthorized": {status: 401, wantError: "unauthorized (HTTP 401)"},
		"rate limited": {
			status:      403,
			headers:     map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
			wantError:   "rate_limited (HTTP 403)",
			wantWaiting: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			databaseURL := newDatabase(t)
			fake := newFakeGitHub(t)
			fake.pullsFail, fake.pullsFailHeaders = test.status, test.headers
			vars := serveVars(t, databaseURL)
			githubVars(t, vars, fake)
			s := startRun(t, vars)
			s.waitReady()

			entry := s.waitGitHubStatus("the failed cycle", func(e map[string]any) bool { return e["lastError"] != nil })

			if entry["lastError"] != test.wantError || entry["lastSuccess"] != nil || (entry["waitUntil"] != nil) != test.wantWaiting {
				t.Fatalf("/status github entry = %v; want error %q, no success, waiting %v", entry, test.wantError, test.wantWaiting)
			}
			if _, raw := s.get("/status"); strings.Contains(raw, fakeErrorBody) {
				t.Fatalf("GET /status carries text GitHub sent: %s", raw)
			}
			if got := scalar(t, databaseURL, "SELECT count(*)::text FROM github_pulls"); got != "0" {
				t.Fatalf("%s pulls observed by a failed cycle", got)
			}
			if code := s.stop(); code != 0 {
				t.Fatalf("a clean stop exited %d:\n%s", code, s.logs.String())
			}
			if strings.Contains(s.logs.String(), fakeErrorBody) || strings.Contains(s.logs.String(), fakeToken) {
				t.Fatalf("the logs carry text GitHub sent or the token:\n%s", s.logs.String())
			}
			kind, status, _ := strings.Cut(strings.TrimSuffix(test.wantError, ")"), " (HTTP ")
			requireOneFailureLog(t, s.logs.String(), kind, status)
		})
	}
}

func TestServeVerifiesProtectionAtStartAndReportsItInStatusAndMCP(t *testing.T) {
	databaseURL := newDatabase(t)
	fake := newFakeGitHub(t)
	vars := serveVars(t, databaseURL)
	githubVars(t, vars, fake)
	mcpServeVars(vars)
	s := startRun(t, vars)
	s.waitReady()

	entry := s.waitGitHubStatus("the first protection verification", func(e map[string]any) bool { return e["protection"] != nil })

	// Before setup there is no test check: the empty one gives missing-test-check, next to the missing contract check.
	want := protection.Evaluate(protection.Facts{}, 1, "")
	got, _ := entry["protection"].(map[string]any)
	if got["result"] != string(want.Kind) || !reflect.DeepEqual(got["findings"], toAny(wantStrings(want.Findings))) || !reflect.DeepEqual(got["caveats"], []any{}) {
		t.Fatalf("/status protection = %v; want the evaluation of an unprotected branch: %+v", got, want)
	}
	if checked, _ := got["checkedAt"].(string); checked == "" {
		t.Fatalf("/status protection = %v; want checkedAt", got)
	} else if _, err := time.Parse(time.RFC3339, checked); err != nil {
		t.Fatalf("checkedAt %q is not RFC 3339: %v", checked, err)
	}
	_, text := callMCP(t, s.mcpSession(), "status", nil)
	var view struct {
		Repositories []struct {
			Protection struct {
				Result   string   `json:"result"`
				Findings []string `json:"findings"`
			} `json:"protection"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(text), &view); err != nil || len(view.Repositories) != 1 || view.Repositories[0].Protection.Result != string(want.Kind) ||
		!slices.Equal(view.Repositories[0].Protection.Findings, wantStrings(want.Findings)) {
		t.Fatalf("MCP status = %s (%v); want the same protection result", text, err)
	}
	fake.mu.Lock()
	protectionRequests := fake.protectionTokenRequests
	fake.mu.Unlock()
	wantRequest := map[string]any{"repositories": []any{"widgets"}, "permissions": map[string]any{"metadata": "read", "administration": "read"}}
	if len(protectionRequests) == 0 || !reflect.DeepEqual(protectionRequests[0], wantRequest) {
		t.Fatalf("protection token requests = %v; want only Metadata and Administration read: %v", protectionRequests, wantRequest)
	}
	// The verification is the service's bookkeeping, like the tick and the relay: it is not counted.
	waitFor(t, "the protection job to finish", 30*time.Second, func() bool {
		return scalar(t, databaseURL, "SELECT count(*)::text FROM river_job WHERE kind = 'github_protection_check' AND state = 'completed'") == "1"
	})
	zeroJobs := map[string]any{"available": 0.0, "running": 0.0, "retryable": 0.0, "scheduled": 0.0, "completed": 0.0, "discarded": 0.0}
	if jobs := s.status()["jobs"]; !reflect.DeepEqual(jobs, zeroJobs) {
		t.Fatalf("GET /status jobs = %v; want the protection check excluded", jobs)
	}
	if code := s.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, s.logs.String())
	}
	if strings.Contains(s.logs.String(), fakeProtectionToken) {
		t.Errorf("the logs contain the protection token:\n%s", s.logs.String())
	}
}

func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// requireOneFailureLog requires one warn line per recorded failure: repo, kind and status, nothing GitHub sent.
func requireOneFailureLog(t *testing.T, logs, kind, status string) {
	t.Helper()
	var warnings int
	for _, line := range strings.Split(logs, "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != "github synchronization failed" {
			continue
		}
		warnings++
		if entry["level"] != "WARN" || entry["repo"] != "acme/widgets" || entry["kind"] != kind || fmt.Sprint(entry["status"]) != status {
			t.Errorf("failure log = %v; want a warning with repo acme/widgets, kind %s and status %s", entry, kind, status)
		}
	}
	if warnings != 1 {
		t.Fatalf("%d failure log lines; want exactly one:\n%s", warnings, logs)
	}
}
