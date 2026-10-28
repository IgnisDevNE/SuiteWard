package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// fivePermissions are the repository permissions of the install guide, exactly.
var fivePermissions = map[string]string{"metadata": "read", "pull_requests": "write", "contents": "read", "administration": "read", "checks": "write"}

// selfCheckGitHubFake is setupGitHub with the two endpoints that only the App JWT reaches: GET /app and GET /app/installations.
type selfCheckGitHubFake struct {
	*setupGitHub
	server *httptest.Server

	appStatus           int               // answer to GET /app, 200 unless set
	installationsStatus int               // answer to GET /app/installations, 200 unless set
	installationID      int64             // the installation listed, setupInstallID unless set
	permissions         map[string]string // the permissions of the listed installation, fivePermissions unless set
	denyPermission      string            // a token request that names this permission is answered 422
	covered             []string          // the repositories (names without the owner) the installation covers, widgets unless set
	malformedListing    bool              // GET /app/installations answers a body that is not a list
	repositoriesStatus  int               // answer to GET /installation/repositories, 200 unless set
	endlessRepositories bool              // GET /installation/repositories answers full pages for ever

	mu      sync.Mutex
	handled []string // "METHOD path" of every request
}

func newSelfCheckGitHub(t *testing.T) *selfCheckGitHubFake {
	t.Helper()
	f := &selfCheckGitHubFake{setupGitHub: newSetupGitHub(t)}
	f.rules = pinnedRules
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *selfCheckGitHubFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.handled = append(f.handled, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/app":
		if f.appStatus != 0 {
			w.WriteHeader(f.appStatus)
			_, _ = w.Write([]byte(setupGitHubBody))
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"slug":"suiteward-test"}`))
	case r.URL.Path == "/app/installations":
		if f.installationsStatus != 0 {
			w.WriteHeader(f.installationsStatus)
			_, _ = w.Write([]byte(setupGitHubBody))
			return
		}
		if f.malformedListing {
			_, _ = w.Write([]byte(`<html>`))
			return
		}
		id, permissions := f.installationID, f.permissions
		if id == 0 {
			id = setupInstallID
		}
		if permissions == nil {
			permissions = fivePermissions
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": id, "account": map[string]any{"login": "octo", "type": "User"}, "repository_selection": "selected", "permissions": permissions,
		}})
	case r.URL.Path == "/installation/repositories":
		if f.repositoriesStatus != 0 {
			w.WriteHeader(f.repositoriesStatus)
			_, _ = w.Write([]byte(setupGitHubBody))
			return
		}
		var repositories []map[string]any
		if f.endlessRepositories {
			for i := range 100 {
				repositories = append(repositories, map[string]any{"full_name": fmt.Sprintf("acme/r%03d", i)})
			}
		}
		for _, name := range f.coveredNames() {
			repositories = append(repositories, map[string]any{"full_name": "acme/" + name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repositories), "repositories": repositories})
	case r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.Unmarshal(body, &request) // a body that is not JSON asks for no repository
		refused := f.denyPermission != "" && strings.Contains(string(body), `"`+f.denyPermission+`"`)
		for _, name := range request.Repositories { // GitHub refuses a token for a repository outside the installation
			refused = refused || !slices.Contains(f.coveredNames(), name)
		}
		if refused {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(setupGitHubBody))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.setupGitHub.serve(w, r)
	default:
		f.setupGitHub.serve(w, r)
	}
}

func (f *selfCheckGitHubFake) settings() config.GitHub {
	g := f.setupGitHub.settings()
	g.APIURL = f.server.URL
	return g
}

func (f *selfCheckGitHubFake) vars() map[string]string {
	vars := f.setupGitHub.vars()
	vars["SUITEWARD_GITHUB_API_URL"] = f.server.URL
	return vars
}

func (f *selfCheckGitHubFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.handled)
}

// enrolledProjects is what setup stores for the fake's repository.
var enrolledProjects = []owner.ProjectPolicy{{RepositoryID: setupRepoID, Repo: setupWidgetsRepo, TestCheck: setupTestCheck}}

const protectionName = "protection." + setupWidgetsRepo

func byName(t *testing.T, results []selfCheckResult) map[string]selfCheckResult {
	t.Helper()
	found := make(map[string]selfCheckResult, len(results))
	for _, r := range results {
		if _, twice := found[r.Name]; twice {
			t.Fatalf("check %s is reported twice: %+v", r.Name, results)
		}
		found[r.Name] = r
	}
	return found
}

// wantOnly asserts which of the named checks failed and that every failure says what is wrong and how to fix it.
func wantFailures(t *testing.T, results []selfCheckResult, failing ...string) map[string]selfCheckResult {
	t.Helper()
	found := byName(t, results)
	for _, name := range []string{"github.app", "github.installation", "github.permissions", "github.repositories", protectionName} {
		r, listed := found[name]
		if !listed {
			t.Fatalf("no result for %s in %+v", name, results)
		}
		if shouldFail := slices.Contains(failing, name); r.OK == shouldFail {
			t.Errorf("%s ok = %v, want %v: %+v", name, r.OK, !shouldFail, r)
		}
		if !r.OK && (r.Detail == "" || r.Fix == "") {
			t.Errorf("%s fails without a detail and a fix: %+v", name, r)
		}
	}
	return found
}

// noSecret fails when a result holds the text GitHub sent with an error or an installation token.
func noSecret(t *testing.T, results []selfCheckResult) {
	t.Helper()
	text, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{setupGitHubBody, setupReadAuth, setupProtAuth, setupKey()[:60]} {
		if strings.Contains(string(text), secret) {
			t.Errorf("the results hold %q: %s", secret, text)
		}
	}
}

func TestSelfCheckGitHubPassesAndOnlyReads(t *testing.T) {
	fake := newSelfCheckGitHub(t)

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	wantFailures(t, results)
	if got := byName(t, results)[protectionName].Detail; !strings.Contains(got, "enforced") {
		t.Errorf("protection detail = %q; want enforced", got)
	}
	for _, request := range fake.requests() {
		if request != "GET /app" && request != "GET /app/installations" && request != "GET /installation/repositories" && !strings.HasPrefix(request, "GET /repos/") &&
			request != fmt.Sprintf("POST /app/installations/%d/access_tokens", setupInstallID) {
			t.Errorf("check made the request %q; it must only read and mint tokens", request)
		}
	}
}

func TestSelfCheckGitHubStopsAtTheAppWhenItsJWTIsRefused(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.appStatus = http.StatusUnauthorized

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	found := wantFailures(t, results, "github.app", "github.installation", "github.permissions", "github.repositories", protectionName)
	if app := found["github.app"]; !strings.Contains(app.Detail, "401") || !strings.Contains(app.Fix, "SUITEWARD_GITHUB_APP") {
		t.Errorf("github.app = %+v; want the status and the settings to fix", app)
	}
	if !strings.Contains(found["github.installation"].Detail, "not checked") {
		t.Errorf("github.installation = %+v; want it not checked", found["github.installation"])
	}
	noSecret(t, results)
}

func TestSelfCheckGitHubReportsAKeyThatIsNotAKeyWithoutPrintingIt(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	g := fake.settings()
	g.AppKey = config.Secret("not-a-pem-GARBAGE")

	results := selfCheckGitHubResults(t.Context(), g, enrolledProjects)

	wantFailures(t, results, "github.app", "github.installation", "github.permissions", "github.repositories", protectionName)
	if text, _ := json.Marshal(results); strings.Contains(string(text), "GARBAGE") {
		t.Errorf("the results hold the key: %s", text)
	}
	if n := len(fake.requests()); n != 0 {
		t.Errorf("GitHub was called %d times with no usable key", n)
	}
}

func TestSelfCheckGitHubReportsAnInstallationTheAppDoesNotHave(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.installationID = 99

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	found := wantFailures(t, results, "github.installation", "github.permissions", "github.repositories", protectionName)
	if got := found["github.installation"]; !strings.Contains(got.Detail, "42") || !strings.Contains(got.Detail, "99") || !strings.Contains(got.Fix, "SUITEWARD_GITHUB_INSTALLATION_ID") {
		t.Errorf("github.installation = %+v; want the configured and the listed id, and the setting to fix", got)
	}
}

func TestSelfCheckGitHubReportsAnInstallationListThatCannotBeRead(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.installationsStatus = http.StatusForbidden

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	found := wantFailures(t, results, "github.installation", "github.permissions", "github.repositories", protectionName)
	if !strings.Contains(found["github.installation"].Detail, "403") {
		t.Errorf("github.installation = %+v; want the status", found["github.installation"])
	}
	noSecret(t, results)
}

func TestSelfCheckGitHubNamesTheMissingPermission(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.permissions = map[string]string{"metadata": "read", "pull_requests": "write", "contents": "read", "checks": "write"}
	fake.denyPermission = "administration"

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	found := wantFailures(t, results, "github.permissions", protectionName)
	if got := found["github.permissions"]; !strings.Contains(got.Detail, "administration: read") || !strings.Contains(got.Fix, "administration: read") {
		t.Errorf("github.permissions = %+v; want the missing permission named in the detail and the fix", got)
	}
	noSecret(t, results)
}

func TestSelfCheckGitHubNamesAPermissionTheAppHasOnlyAtALowerLevel(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.permissions = map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read", "administration": "read", "checks": "write"}
	fake.denyPermission = "pull_requests"

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	if got := wantFailures(t, results, "github.permissions", "github.repositories", protectionName)["github.permissions"]; !strings.Contains(got.Detail, "pull_requests: write") {
		t.Errorf("github.permissions = %+v; want pull_requests: write named", got)
	}
}

func TestSelfCheckGitHubRefusesAnAppThatGrantsMoreThanItUses(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"contents write":   {"contents": "write"},
		"administration":   {"administration": "write"},
		"an unused scope":  {"issues": "read"},
		"workflows access": {"workflows": "write"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSelfCheckGitHub(t)
			fake.permissions = map[string]string{}
			for k, v := range fivePermissions {
				fake.permissions[k] = v
			}
			for k, v := range extra {
				fake.permissions[k] = v
			}

			results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

			found := wantFailures(t, results, "github.permissions")
			for k, v := range extra {
				if got := found["github.permissions"]; !strings.Contains(got.Detail, k+": "+v) || !strings.Contains(got.Fix, "remove") {
					t.Errorf("github.permissions = %+v; want %s: %s named as excess and the fix to remove it", got, k, v)
				}
			}
		})
	}
}

func TestSelfCheckGitHubReportsATokenWhoseGrantedPermissionsDiffer(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.mismatch = true

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	got := wantFailures(t, results, "github.permissions", protectionName)["github.permissions"]
	if !strings.Contains(got.Detail, "protection token was granted other permissions") || !strings.Contains(got.Fix, "administration: read, metadata: read") {
		t.Errorf("github.permissions = %+v; want the token and, in the fix, the permissions it requests (SuiteWard's own, not GitHub's answer)", got)
	}
	noSecret(t, results)
}

func TestSelfCheckGitHubNamesARepositoryTheInstallationDoesNotCover(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.covered = []string{"widgets"}
	g := fake.settings()
	g.Repositories = []string{setupWidgetsRepo, "acme/other"}

	results := selfCheckGitHubResults(t.Context(), g, enrolledProjects)

	found := byName(t, results)
	repositories := found["github.repositories"]
	if repositories.OK || !strings.Contains(repositories.Detail, "acme/other") || strings.Contains(repositories.Detail, setupWidgetsRepo) || !strings.Contains(repositories.Fix, "installation") {
		t.Errorf("github.repositories = %+v; want exactly the uncovered repository named, and the installation's repository access to fix", repositories)
	}
	if permissions := found["github.permissions"]; permissions.OK || !strings.Contains(permissions.Detail, "not checked") {
		t.Errorf("github.permissions = %+v; GitHub refuses every token that names an uncovered repository, so it is not checked", permissions)
	}
	if covered := found[protectionName]; !covered.OK {
		t.Errorf("%s = %+v; a covered repository is still checked", protectionName, covered)
	}
	if uncovered := found["protection.acme/other"]; uncovered.OK || !strings.Contains(uncovered.Detail, "not checked") {
		t.Errorf("protection.acme/other = %+v; want it not checked", uncovered)
	}
	noSecret(t, results)
}

func TestSelfCheckGitHubReportsAnInstallationThatCoversNoConfiguredRepository(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.covered = []string{}
	g := fake.settings()

	results := selfCheckGitHubResults(t.Context(), g, enrolledProjects)

	found := wantFailures(t, results, "github.permissions", "github.repositories", protectionName)
	if got := found["github.repositories"]; !strings.Contains(got.Detail, setupWidgetsRepo) {
		t.Errorf("github.repositories = %+v; want the repository named as not covered", got)
	}
}

func TestSelfCheckGitHubReportsARepositoryTheInstallationCannotRead(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.repoStatus = http.StatusNotFound

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	found := wantFailures(t, results, "github.repositories", protectionName)
	if got := found["github.repositories"]; !strings.Contains(got.Detail, setupWidgetsRepo) || !strings.Contains(got.Detail, "404") || !strings.Contains(got.Fix, "installation") {
		t.Errorf("github.repositories = %+v; want the repository, the status and the installation to fix", got)
	}
	if !strings.Contains(found[protectionName].Detail, "not checked") {
		t.Errorf("%s = %+v; want it not checked", protectionName, found[protectionName])
	}
	noSecret(t, results)
}

func TestSelfCheckReportsEachProtectionResult(t *testing.T) {
	for name, test := range map[string]struct {
		mutate   func(*selfCheckGitHubFake)
		projects []owner.ProjectPolicy
		ok       bool
		details  []string // every part must be in the detail
		fix      string
	}{
		"enforced": {func(*selfCheckGitHubFake) {}, enrolledProjects, true, []string{"enforced"}, ""},
		"findings": {
			func(f *selfCheckGitHubFake) { f.rules = "[]" }, enrolledProjects, false,
			[]string{"findings", "missing-contract-check", "missing-test-check"}, "SuiteWard / Contract",
		},
		"the test check comes from the enrollment": {func(*selfCheckGitHubFake) {}, nil, false, []string{"findings", "missing-test-check"}, "test check"},
		"enforcement unavailable": {
			func(f *selfCheckGitHubFake) { f.private, f.rulesStatus = true, http.StatusForbidden }, enrolledProjects, false,
			[]string{"enforcement-unavailable"}, "GitHub Free",
		},
		"unknown": {func(f *selfCheckGitHubFake) { f.rulesStatus = http.StatusInternalServerError }, enrolledProjects, false, []string{"unknown"}, "Administration read"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newSelfCheckGitHub(t)
			test.mutate(fake)

			results := selfCheckGitHubResults(t.Context(), fake.settings(), test.projects)

			got := byName(t, results)[protectionName]
			missing := slices.ContainsFunc(test.details, func(part string) bool { return !strings.Contains(got.Detail, part) })
			if got.OK != test.ok || missing || !strings.Contains(got.Fix, test.fix) {
				t.Errorf("%s = %+v; want ok %v, detail with %q and fix %q", protectionName, got, test.ok, test.details, test.fix)
			}
			noSecret(t, results)
		})
	}
}

func TestWriteSelfCheckPrintsOneLinePerCheck(t *testing.T) {
	results := []selfCheckResult{
		{Name: "github.app", OK: true, Detail: "app suiteward-test"},
		{Name: "database.role", Detail: "role app is a superuser", Fix: "connect as the application role"},
	}
	var out bytes.Buffer

	if err := writeSelfCheck(&out, results, false); err != nil {
		t.Fatal(err)
	}

	want := "ok github.app\nFAIL database.role: role app is a superuser — connect as the application role\n"
	if out.String() != want {
		t.Errorf("output = %q; want %q", out.String(), want)
	}
}

func TestWriteSelfCheckJSONIsAnArrayOfNameOkDetailFix(t *testing.T) {
	results := []selfCheckResult{{Name: "github.app", OK: true}, {Name: "database.role", Detail: "d", Fix: "f"}}
	var out bytes.Buffer

	if err := writeSelfCheck(&out, results, true); err != nil {
		t.Fatal(err)
	}

	var got []map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q is not a JSON array: %v", out.String(), err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries; want 2", len(got))
	}
	for i, entry := range got {
		keys := make([]string, 0, len(entry))
		for k := range entry {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"detail", "fix", "name", "ok"}) {
			t.Errorf("entry %d has keys %v; want exactly name, ok, detail and fix, always", i, keys)
		}
	}
	if got[0]["ok"] != true || got[1]["ok"] != false || got[1]["fix"] != "f" {
		t.Errorf("entries = %v", got)
	}
}

// selfCheckVars are the settings of a check over fake with a database that cannot be reached.
func selfCheckVars(t *testing.T, fake *selfCheckGitHubFake) map[string]string {
	t.Helper()
	vars := fake.vars()
	vars["SUITEWARD_DATABASE_URL"] = "postgres://suiteward:" + leakWord + "@127.0.0.1:1/suiteward?sslmode=disable"
	vars["SUITEWARD_ARTIFACT_DIR"] = t.TempDir()
	return vars
}

func TestSelfCheckUsageErrorsExitWithTwoBeforeAnythingIsCalled(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"stray argument": {args: []string{"stray"}, want: "stray"},
		"unknown flag":   {args: []string{"--nope"}, want: "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := run(append([]string{"check"}, test.args...), testEnv(selfCheckVars(t, fake)))

			if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") || !strings.Contains(stderr, test.want) {
				t.Fatalf("check %q = %d, stdout %q, stderr %q; want 2, nothing on stdout and a usage message naming %q", test.args, code, stdout, stderr, test.want)
			}
			if n := len(fake.requests()); n != 0 {
				t.Fatalf("GitHub was called %d times by a usage error", n)
			}
		})
	}
}

func TestSelfCheckRefusesTheMigrationCredential(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	vars := selfCheckVars(t, fake)
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = "postgres://owner@127.0.0.1:1/db?sslmode=disable"

	code, stdout, stderr := run([]string{"check"}, testEnv(vars))

	if code != 1 || stdout != "" || !strings.Contains(stderr, "migration credential") {
		t.Fatalf("check with the migration credential = %d, stdout %q, stderr %q; want 1 and the refusal", code, stdout, stderr)
	}
	if n := len(fake.requests()); n != 0 {
		t.Fatalf("GitHub was called %d times", n)
	}
}

func TestSelfCheckExitsOneAndReportsEveryCheckWhenTheDatabaseIsDown(t *testing.T) {
	fake := newSelfCheckGitHub(t)

	code, stdout, stderr := run([]string{"check"}, testEnv(selfCheckVars(t, fake)))

	if code != 1 {
		t.Fatalf("check = %d, stderr %q; want 1 with the database down", code, stderr)
	}
	for _, line := range []string{"ok github.app", "ok github.installation", "ok github.permissions", "ok github.repositories", "FAIL owner.enrolled: ", "FAIL database.schema: ", "FAIL database.role: "} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}
	if !strings.Contains(stdout, "FAIL "+protectionName+": ") {
		t.Errorf("stdout lacks the failed protection check (no test check without the enrollment):\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, leakWord) {
		t.Errorf("the database password is in the output:\n%s\n%s", stdout, stderr)
	}
}

func TestSelfCheckJSONFlagPrintsTheArrayAndExitsOne(t *testing.T) {
	fake := newSelfCheckGitHub(t)

	code, stdout, stderr := run([]string{"check", "--json"}, testEnv(selfCheckVars(t, fake)))

	if code != 1 {
		t.Fatalf("check --json = %d, stderr %q; want 1", code, stderr)
	}
	var results []selfCheckResult
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("stdout %q is not a JSON array of results: %v", stdout, err)
	}
	found := byName(t, results)
	for _, name := range []string{"github.app", "github.installation", "github.permissions", "github.repositories", protectionName, "owner.enrolled", "database.schema", "database.role"} {
		if _, listed := found[name]; !listed {
			t.Errorf("no result for %s in %s", name, stdout)
		}
	}
	if !found["github.app"].OK || found["database.schema"].OK {
		t.Errorf("results = %+v; want github.app ok and database.schema failed", found)
	}
	if strings.Contains(stdout+stderr, leakWord) {
		t.Errorf("the database password is in the output:\n%s\n%s", stdout, stderr)
	}
}

func (f *selfCheckGitHubFake) coveredNames() []string {
	if f.covered == nil {
		return []string{"widgets"}
	}
	return f.covered
}

func TestSelfCheckGitHubSaysWhenAListingIsMalformedOrTooLong(t *testing.T) {
	t.Run("a malformed installations answer", func(t *testing.T) {
		fake := newSelfCheckGitHub(t)
		fake.malformedListing = true

		got := byName(t, selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects))["github.installation"]

		if got.OK || !strings.Contains(got.Detail, "malformed") || strings.Contains(got.Detail, "transport") {
			t.Errorf("github.installation = %+v; want a malformed answer, not a transport failure", got)
		}
	})
	t.Run("more repositories than can be listed", func(t *testing.T) {
		fake := newSelfCheckGitHub(t)
		fake.endlessRepositories = true

		got := byName(t, selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects))["github.repositories"]

		if got.OK || !strings.Contains(got.Detail, "more than 1000 repositories") || !strings.Contains(got.Fix, "selected repositories") || strings.Contains(got.Detail, "transport") {
			t.Errorf("github.repositories = %+v; want the limit named and selected repositories as the fix", got)
		}
	})
}

// silentServer accepts connections and never answers: the way a black-holed database host looks once the dial succeeds.
func silentServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close() // the test is over; the accept loop ends with the error
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close() // idem
		}
	})
	return listener.Addr().String()
}

func TestSelfCheckDatabaseGivesUpOnAHostThatNeverAnswers(t *testing.T) {
	address := silentServer(t)
	cfg, _, err := config.Load(func(name string) (string, bool) {
		value, ok := map[string]string{
			"SUITEWARD_DATABASE_URL": "postgres://suiteward:" + leakWord + "@" + address + "/suiteward?sslmode=disable",
			"SUITEWARD_ARTIFACT_DIR": t.TempDir(),
		}[name]
		return value, ok
	}, nil, func(string) ([]byte, error) { return nil, errors.New("no files") })
	if err != nil {
		t.Fatal(err)
	}
	previous := selfCheckDatabaseTimeout
	selfCheckDatabaseTimeout = 300 * time.Millisecond
	t.Cleanup(func() { selfCheckDatabaseTimeout = previous })

	started := time.Now()
	schema, role, enrolled, _ := selfCheckDatabaseResults(t.Context(), cfg)

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the database checks took %v against a host that never answers; want them bounded by the timeout", elapsed)
	}
	for _, r := range []selfCheckResult{schema, role, enrolled} {
		if r.OK || r.Detail == "" || r.Fix == "" || strings.Contains(r.Detail+r.Fix, leakWord) {
			t.Errorf("%+v; want a failure with a detail and a fix, and no password", r)
		}
	}
}

// githubSettingNames are the settings of the GitHub part of the service: an instance installed without GitHub has none of them.
var githubSettingNames = []string{"SUITEWARD_GITHUB_APP_ID", "SUITEWARD_GITHUB_APP_KEY", "SUITEWARD_GITHUB_INSTALLATION_ID", "SUITEWARD_GITHUB_REPOSITORIES", "SUITEWARD_GITHUB_API_URL"}

// The install allows "GitHub is not configured": check then runs the database checks and reports each GitHub check as not
// configured, a warning that does not fail it, instead of refusing to run.
func TestSelfCheckWithoutGitHubChecksTheDatabaseAndWarnsAboutGitHub(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	vars := selfCheckVars(t, fake)
	for _, name := range githubSettingNames {
		delete(vars, name)
	}

	code, stdout, stderr := run([]string{"check", "--json"}, testEnv(vars))

	if code != 1 || stderr != "" { // the database of selfCheckVars cannot be reached, which is what fails
		t.Fatalf("check = %d, stderr %q; want 1 (the database is down) and no usage error", code, stderr)
	}
	var results []selfCheckResult
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("stdout %q is not a JSON array: %v", stdout, err)
	}
	found := byName(t, results)
	for _, name := range []string{"github.app", "github.installation", "github.permissions", "github.repositories"} {
		if r, listed := found[name]; !listed || !r.OK || !r.Warning || !strings.Contains(r.Detail, "not configured") || r.Fix == "" {
			t.Errorf("%s = %+v; want a passing warning that says GitHub is not configured and how to set it up", name, r)
		}
	}
	for _, name := range []string{"database.schema", "database.role", "owner.enrolled"} {
		if r, listed := found[name]; !listed || r.OK {
			t.Errorf("%s = %+v; want the database check to have run and failed (the database is down)", name, r)
		}
	}
	for name := range found {
		if strings.HasPrefix(name, "protection.") {
			t.Errorf("%s is reported without a repository to protect", name)
		}
	}
	if n := len(fake.requests()); n != 0 {
		t.Errorf("GitHub was called %d times", n)
	}
}

// The fix is shown by `doctor` on both tiers. A co-located PC has no host, and on a Podman machine the containers run in a VM
// while the install runs on the PC, so the fix names no place: the install re-run finds its tier in the saved settings.
func TestNotConfiguredFixNamesNoPlace(t *testing.T) {
	r := checkNotConfigured("github.app")

	if !strings.Contains(r.Fix, "again with --github yes") || strings.Contains(r.Fix, "on the host") || strings.Contains(r.Fix, "on the machine") {
		t.Errorf("fix = %q; want the install again with --github yes, and no place named", r.Fix)
	}
}

func TestWriteSelfCheckPrintsAWarningWithItsFix(t *testing.T) {
	results := []selfCheckResult{{Name: "github.app", OK: true, Warning: true, Detail: "not configured", Fix: "set GitHub up"}}
	var out bytes.Buffer

	if err := writeSelfCheck(&out, results, false); err != nil {
		t.Fatal(err)
	}

	if want := "WARN github.app: not configured — set GitHub up\n"; out.String() != want {
		t.Errorf("output = %q; want %q", out.String(), want)
	}
}

// A fix that names a command of this program by its bare name says so in the row, as data: the installer that runs doctor knows which instance it
// works on, and writes the command with that instance's flags. The sentence of the fix stays as it is, for a reader that does not know the key.
func TestTheRowsWhoseFixNamesACommandSayWhichOnesInTheJSON(t *testing.T) {
	enrolled := protection.Evaluate(protection.Facts{}, 1234, "")
	withTest := protection.Evaluate(protection.Facts{}, 1234, "CI / test")
	for name, c := range map[string]struct {
		row  selfCheckResult
		want string // the text of the key in the JSON, empty for none
	}{
		"GitHub is not configured":            {checkNotConfigured(nameApp), `"commands":["install"]`},
		"no owner is enrolled":                {checkNoOwner(), `"commands":["enroll","install"]`},
		"no test check is configured yet":     {selfCheckProtection("Acme/api", enrolled), `"commands":["enroll"]`},
		"a test check that the rules lack":    {selfCheckProtection("Acme/api", withTest), ""},
		"a row whose fix names no command":    {checkFailed(nameRole, "d", "f"), ""},
		"a check that waits for another one":  {checkSkipped(nameOwnerEnrolled, nameSchema), ""},
		"the database that cannot be reached": {checkFailed(nameSchema, "cannot reach the database", dbFix), ""},
	} {
		raw, err := json.Marshal(c.row)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(raw), `"commands"`); c.want == "" && has || c.want != "" && !strings.Contains(string(raw), c.want) {
			t.Errorf("%s: the row is %s, want the key %q", name, raw, c.want)
		}
	}
}
