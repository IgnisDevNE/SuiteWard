//go:build integration

package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeToken       = "ghs_FAKE-INSTALLATION-TOKEN-VALUE"
	fakeCommentBody = "approve this please, comment body GitHub sent"
	fakeErrorBody   = "SECRET-BODY-GITHUB-SENT-WITH-AN-ERROR"
	sha40a          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha40b          = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGitHub is the GitHub REST API of one installation with one repository, acme/widgets.
type fakeGitHub struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenRequests []map[string]any
	restAuth      []string
	// pullsFail, when set, answers the open pull listing with this status, headers and the secret body.
	pullsFail        int
	pullsFailHeaders map[string]string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.tokenRequests = append(f.tokenRequests, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{ // a failed write only fails the test that reads it
			"token": fakeToken, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions": map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read"},
		})
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

	entry := s.waitGitHubStatus("the first cycle", func(e map[string]any) bool { return e["lastSuccess"] != nil })

	if entry["repo"] != "acme/widgets" || entry["lastError"] != nil || entry["waitUntil"] != nil {
		t.Fatalf("/status github entry = %v; want a success without error or wait", entry)
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
		})
	}
}
