package installer

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeAppID          = 4242
	fakeInstallationID = 77
)

var (
	fakeKeyOnce sync.Once
	fakeKeyPEM  []byte
)

// testKeyPEM is one RSA key for the whole test binary; generating one per test is slow.
func testKeyPEM() []byte {
	fakeKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		fakeKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	})
	return fakeKeyPEM
}

// pemBody returns a line of the key's base64 that no other text of a test could contain.
func pemBody() string { return strings.Split(string(testKeyPEM()), "\n")[2] }

type fakeRepo struct {
	name        string // owner/name
	private     bool
	branch      string
	checkRuns   []string
	rulesStatus int           // 0 is 200
	ruleChecks  []fakeRuleChk // the required checks of the one ruleset rule
	ruleStrict  bool
	noRule      bool // no required_status_checks rule at all
}

type fakeRuleChk struct {
	context       string
	integrationID int64
}

// syncBuffer is an output buffer that the flow and the test can use at the same time.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeGitHub is the GitHub API that the GitHub flow talks to.
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string // "METHOD path", in order
	polls    int      // calls of GET /app/installations
	headers  map[string]http.Header

	ownerLogin, ownerType string
	convertRaw            string              // when set, the 201 answer of the conversion is this text
	tokenRequests         []map[string]string // the permissions each access_tokens request asked for
	checkRunAuth          string              // the Authorization header of the last check-runs read
	convertPerms          map[string]string
	convertStatus         int
	appStatus             int // GET /app answers this status when set (the App was deleted: 404; the key is refused: 401)
	installAfter          int // GET /app/installations answers an empty list for this many calls
	installAccount        string
	installType           string
	repos                 []fakeRepo
	override              map[string]fakeAnswer // "METHOD path" -> the answer given instead of the fake's own
}

// fakeAnswer replaces what the fake says to one request.
type fakeAnswer struct {
	status int
	body   string
}

var appPermissions = map[string]string{"metadata": "read", "pull_requests": "write", "contents": "read", "administration": "read", "checks": "write"}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{
		headers: map[string]http.Header{}, ownerLogin: "acme", ownerType: "Organization", convertPerms: appPermissions,
		convertStatus: http.StatusCreated,
		repos: []fakeRepo{{name: "acme/app", branch: "main", checkRuns: []string{"CI / test", "SuiteWard / Contract", "lint", "CI / test"},
			ruleChecks: []fakeRuleChk{{"SuiteWard / Contract", fakeAppID}, {"CI / test", 0}}, ruleStrict: true}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", g.convert)
	mux.HandleFunc("GET /app", g.app)
	mux.HandleFunc("GET /app/installations", g.installations)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", g.token)
	mux.HandleFunc("GET /installation/repositories", g.installationRepos)
	mux.HandleFunc("GET /repos/{owner}/{name}", g.repo)
	mux.HandleFunc("GET /repos/{owner}/{name}/commits/{branch}/check-runs", g.checkRunList)
	mux.HandleFunc("GET /repos/{owner}/{name}/rules/branches/{branch}", g.rules)
	mux.HandleFunc("GET /repos/{owner}/{name}/branches/{branch}/protection", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.requests = append(g.requests, r.Method+" "+r.URL.Path)
		g.headers[r.Method+" "+r.URL.Path] = r.Header.Clone()
		answer, overridden := g.override[r.Method+" "+r.URL.Path]
		g.mu.Unlock()
		if overridden {
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(answer.body))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) sawRequest(prefix string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.requests {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func (g *fakeGitHub) header(request string) http.Header {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.headers[request]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *fakeGitHub) convert(w http.ResponseWriter, _ *http.Request) {
	if g.convertRaw != "" {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(g.convertRaw))
		return
	}
	if g.convertStatus != http.StatusCreated {
		w.WriteHeader(g.convertStatus)
		_, _ = w.Write([]byte("leak-me-in-no-error"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": fakeAppID, "slug": "suiteward-abcd", "owner": map[string]any{"login": g.ownerLogin, "type": g.ownerType},
		"permissions": g.convertPerms, "pem": string(testKeyPEM()), "client_secret": "client-secret-value", "webhook_secret": "webhook-secret-value",
	})
}

func (g *fakeGitHub) app(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	status := g.appStatus
	g.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": "suiteward-abcd"})
}

func (g *fakeGitHub) installations(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	g.polls++
	waiting := g.polls <= g.installAfter
	account, typ := g.ownerLogin, g.ownerType
	if g.installAccount != "" {
		account, typ = g.installAccount, g.installType
	}
	g.mu.Unlock()
	if waiting {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, []any{map[string]any{
		"id": fakeInstallationID, "account": map[string]any{"login": account, "type": typ},
		"repository_selection": "selected", "permissions": appPermissions,
	}})
}

func (g *fakeGitHub) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Permissions map[string]string `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	g.tokenRequests = append(g.tokenRequests, req.Permissions)
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"token": fakeTokenFor(req.Permissions), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": req.Permissions})
}

func (g *fakeGitHub) installationRepos(w http.ResponseWriter, _ *http.Request) {
	list := []any{}
	for _, repo := range g.repos {
		list = append(list, map[string]any{"full_name": repo.name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(list), "repositories": list})
}

func (g *fakeGitHub) find(r *http.Request) (fakeRepo, bool) {
	name := r.PathValue("owner") + "/" + r.PathValue("name")
	for _, repo := range g.repos {
		if repo.name == name {
			return repo, true
		}
	}
	return fakeRepo{}, false
}

func (g *fakeGitHub) repo(w http.ResponseWriter, r *http.Request) {
	repo, ok := g.find(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": 1000 + len(repo.name), "full_name": repo.name, "private": repo.private, "default_branch": repo.branch})
}

func (g *fakeGitHub) checkRunList(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.checkRunAuth = r.Header.Get("Authorization")
	g.mu.Unlock()
	repo, ok := g.find(r)
	if !ok || r.PathValue("branch") != repo.branch {
		http.NotFound(w, r)
		return
	}
	runs := []any{}
	for _, name := range repo.checkRuns {
		runs = append(runs, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
}

func (g *fakeGitHub) rules(w http.ResponseWriter, r *http.Request) {
	repo, ok := g.find(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if repo.rulesStatus != 0 {
		w.WriteHeader(repo.rulesStatus)
		return
	}
	if repo.noRule {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	checks := []any{}
	for _, c := range repo.ruleChecks {
		entry := map[string]any{"context": c.context}
		if c.integrationID != 0 {
			entry["integration_id"] = c.integrationID
		}
		checks = append(checks, entry)
	}
	writeJSON(w, http.StatusOK, []any{map[string]any{
		"type": "required_status_checks", "ruleset_source_type": "Repository", "ruleset_id": 9,
		"parameters": map[string]any{"strict_required_status_checks_policy": repo.ruleStrict, "required_status_checks": checks},
	}})
}

var listenerURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/`)

// waitForListener returns the address the flow printed for its loopback listener.
func waitForListener(t *testing.T, out *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := listenerURL.FindString(out.String()); m != "" {
			return m
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the flow never printed its listener address; output:\n%s", out.String())
	return ""
}

var (
	formAction   = regexp.MustCompile(`action="([^"]+)"`)
	formManifest = regexp.MustCompile(`name="manifest" value="([^"]*)"`)
)

// fetchForm GETs the manifest page and returns the form's action URL and manifest JSON.
func fetchForm(t *testing.T, base string) (action string, manifest []byte) {
	t.Helper()
	resp, err := http.Get(base) //nolint:gosec,noctx // a loopback test server
	if err != nil {
		t.Fatalf("GET %s: %v", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the manifest page: %v", err)
	}
	a := formAction.FindSubmatch(page)
	m := formManifest.FindSubmatch(page)
	if a == nil || m == nil {
		t.Fatalf("the manifest page has no form with an action and a manifest:\n%s", page)
	}
	return html.UnescapeString(string(a[1])), []byte(html.UnescapeString(string(m[1])))
}

func stateOf(t *testing.T, action string) string {
	t.Helper()
	_, state, ok := strings.Cut(action, "state=")
	if !ok || state == "" {
		t.Fatalf("the form action %q carries no state", action)
	}
	return state
}

// callback performs the browser's redirect and returns the HTTP status.
func callback(t *testing.T, base, code, state string) int {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%scallback?code=%s&state=%s", base, code, state)) //nolint:gosec,noctx // a loopback test server
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// fakeTokenFor is the token the fake mints for a permission map, so a request shows which map its token came from.
func fakeTokenFor(perms map[string]string) string {
	pairs := make([]string, 0, len(perms))
	for k, v := range perms {
		pairs = append(pairs, k+"="+v)
	}
	slices.Sort(pairs)
	return "tok-" + strings.Join(pairs, ",")
}
