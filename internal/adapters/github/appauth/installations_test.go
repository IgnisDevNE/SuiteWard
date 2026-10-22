package appauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
)

// installationJSON is one entry of GET /app/installations.
func installationJSON(id int, login, accountType, selection string, perms map[string]string) map[string]any {
	return map[string]any{
		"id": id, "account": map[string]any{"login": login, "type": accountType},
		"repository_selection": selection, "permissions": perms,
	}
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// queryLog records the raw queries of the pages a fake was asked for.
type queryLog struct {
	mu      sync.Mutex
	queries []string
}

func (q *queryLog) add(r *http.Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queries = append(q.queries, r.URL.RawQuery)
}

func (q *queryLog) get() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.queries)
}

func TestInstallationsListsTheInstallationsWithTheAppJWT(t *testing.T) {
	t.Parallel()
	perms := map[string]string{"metadata": "read", "checks": "write"}
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) {
		return http.StatusOK, jsonBody(t, []map[string]any{
			installationJSON(7, "octo", "User", "selected", perms),
			installationJSON(8, "acme", "Organization", "all", map[string]string{"metadata": "read"}),
		})
	})

	got, err := newApp(t, &fakeClock{now: t0}).Installations(context.Background(), g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Installations: %v", err)
	}
	want := []appauth.InstallationInfo{
		{ID: 7, Account: "octo", AccountType: "User", RepositorySelection: "selected", Permissions: perms},
		{ID: 8, Account: "acme", AccountType: "Organization", RepositorySelection: "all", Permissions: map[string]string{"metadata": "read"}},
	}
	if len(got) != len(want) {
		t.Fatalf("Installations = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Account != want[i].Account || got[i].AccountType != want[i].AccountType ||
			got[i].RepositorySelection != want[i].RepositorySelection || !maps.Equal(got[i].Permissions, want[i].Permissions) {
			t.Errorf("installation %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != "/app/installations" {
		t.Fatalf("requests = %+v, want one GET /app/installations", reqs)
	}
	_, claims := verifyJWT(t, bearerJWT(t, reqs[0]), &testKey().PublicKey)
	if claims["iss"] != "12345" {
		t.Errorf("JWT claims %v, want the app id as issuer", claims)
	}
}

func TestInstallationsFollowsThePagesByNumberAndStopsOnAShortPage(t *testing.T) {
	t.Parallel()
	var queries queryLog
	g := newFakeGitHub(t, func(_ int, r *http.Request) (int, string) {
		queries.add(r)
		var entries []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			for i := range 100 {
				entries = append(entries, installationJSON(i+1, "octo", "User", "all", map[string]string{"metadata": "read"}))
			}
		case "2":
			entries = append(entries, installationJSON(101, "octo", "User", "all", map[string]string{"metadata": "read"}))
		}
		return http.StatusOK, jsonBody(t, entries)
	})

	got, err := newApp(t, &fakeClock{now: t0}).Installations(context.Background(), g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Installations: %v", err)
	}
	if len(got) != 101 || got[100].ID != 101 {
		t.Fatalf("got %d installations, want 101 ending with id 101", len(got))
	}
	if want := []string{"page=1&per_page=100", "page=2&per_page=100"}; !slices.Equal(queries.get(), want) {
		t.Errorf("queries = %v, want %v", queries.get(), want)
	}
}

func TestInstallationsRefusesMoreThanTheCapInsteadOfTruncating(t *testing.T) {
	t.Parallel()
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) {
		var entries []map[string]any
		for i := range 100 {
			entries = append(entries, installationJSON(i+1, "octo", "User", "all", map[string]string{"metadata": "read"}))
		}
		return http.StatusOK, jsonBody(t, entries)
	})

	got, err := newApp(t, &fakeClock{now: t0}).Installations(context.Background(), g.srv.Client(), g.srv.URL)
	if err == nil {
		t.Fatalf("Installations answered %d entries for endless pages; want an error", len(got))
	}
}

func TestInstallationsFailuresAreErrorsWithoutTheJWT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int // the StatusError status, or 0 for a malformed answer
	}{
		{"401", http.StatusUnauthorized, `{"message":"A JSON web token could not be decoded"}`, 401},
		{"not an array", http.StatusOK, `{"installations":[]}`, 0},
		{"not JSON", http.StatusOK, `<html>`, 0},
		{"no id", http.StatusOK, `[{"account":{"login":"octo","type":"User"}}]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return c.status, c.body })

			_, err := newApp(t, &fakeClock{now: t0}).Installations(context.Background(), g.srv.Client(), g.srv.URL)
			if err == nil {
				t.Fatal("Installations succeeded")
			}
			var status *appauth.StatusError
			switch {
			case c.wantStatus != 0 && (!errors.As(err, &status) || status.Status != c.wantStatus || status.Endpoint != "installations"):
				t.Errorf("err = %v, want *StatusError{installations, %d}", err, c.wantStatus)
			case c.wantStatus == 0 && errors.As(err, &status):
				t.Errorf("err = %v, want a malformed-answer error, not a StatusError", err)
			}
			if reqs := g.requests(); len(reqs) > 0 && strings.Contains(err.Error(), bearerJWT(t, reqs[0])) {
				t.Errorf("error %q holds the JWT", err)
			}
		})
	}
}

// repositoriesFake answers the token request with a metadata token and the repository pages with page(n), where n is the page number.
func repositoriesFake(t *testing.T, queries *queryLog, page func(n string) (int, string)) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return http.StatusCreated, tokenBody(tokenValue(n), t0.Add(time.Hour), map[string]string{"metadata": "read"})
		}
		queries.add(r)
		return page(r.URL.Query().Get("page"))
	})
}

func fullNames(prefix string, n int) []map[string]any {
	var repos []map[string]any
	for i := range n {
		repos = append(repos, map[string]any{"full_name": fmt.Sprintf("%s/r%03d", prefix, i)})
	}
	return repos
}

func TestInstallationRepositoriesMintsAMetadataOnlyTokenWithNoRepositoryListAndPages(t *testing.T) {
	t.Parallel()
	var queries queryLog
	g := repositoriesFake(t, &queries, func(page string) (int, string) {
		repos := fullNames("octo", 100)
		if page == "2" {
			repos = []map[string]any{{"full_name": "octo/last"}}
		}
		return http.StatusOK, jsonBody(t, map[string]any{"total_count": 101, "repositories": repos})
	})

	got, err := newApp(t, &fakeClock{now: t0}).InstallationRepositories(context.Background(), installationID, g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("InstallationRepositories: %v", err)
	}
	if len(got) != 101 || got[0] != "octo/r000" || got[100] != "octo/last" {
		t.Fatalf("got %d repositories, want 101 from octo/r000 to octo/last", len(got))
	}
	reqs := g.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the token request and two pages", len(reqs))
	}
	if reqs[0].method != http.MethodPost || reqs[0].path != fmt.Sprintf("/app/installations/%d/access_tokens", installationID) {
		t.Fatalf("first request = %s %s, want the token request", reqs[0].method, reqs[0].path)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(reqs[0].body, &raw); err != nil {
		t.Fatalf("token request body %q: %v", reqs[0].body, err)
	}
	if _, listed := raw["repositories"]; listed || len(raw) != 1 || string(raw["permissions"]) != `{"metadata":"read"}` {
		t.Errorf("token request body = %s, want only permissions {metadata: read} and no repositories key", reqs[0].body)
	}
	for _, r := range reqs[1:] {
		if r.method != http.MethodGet || r.path != "/installation/repositories" {
			t.Errorf("request %s %s, want GET /installation/repositories", r.method, r.path)
		}
		if got, want := r.header.Get("Authorization"), "Bearer "+tokenValue(1); got != want {
			t.Errorf("page authorized with %q, want the installation token", got)
		}
	}
	if want := []string{"page=1&per_page=100", "page=2&per_page=100"}; !slices.Equal(queries.get(), want) {
		t.Errorf("queries = %v, want %v", queries.get(), want)
	}
}

func TestInstallationRepositoriesRefusesMoreThanTheCapAndReportsFailures(t *testing.T) {
	t.Parallel()
	t.Run("endless pages", func(t *testing.T) {
		t.Parallel()
		g := repositoriesFake(t, &queryLog{}, func(string) (int, string) {
			return http.StatusOK, jsonBody(t, map[string]any{"total_count": 100000, "repositories": fullNames("octo", 100)})
		})
		if got, err := newApp(t, &fakeClock{now: t0}).InstallationRepositories(context.Background(), installationID, g.srv.Client(), g.srv.URL); err == nil {
			t.Fatalf("answered %d repositories for endless pages; want an error", len(got))
		}
	})
	t.Run("token refused", func(t *testing.T) {
		t.Parallel()
		g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return http.StatusNotFound, `{"message":"Not Found"}` })
		_, err := newApp(t, &fakeClock{now: t0}).InstallationRepositories(context.Background(), installationID, g.srv.Client(), g.srv.URL)
		var status *appauth.StatusError
		if !errors.As(err, &status) || status.Status != http.StatusNotFound {
			t.Errorf("err = %v, want *StatusError 404", err)
		}
	})
	t.Run("page refused", func(t *testing.T) {
		t.Parallel()
		g := repositoriesFake(t, &queryLog{}, func(string) (int, string) { return http.StatusForbidden, `{"message":"no"}` })
		_, err := newApp(t, &fakeClock{now: t0}).InstallationRepositories(context.Background(), installationID, g.srv.Client(), g.srv.URL)
		var status *appauth.StatusError
		if !errors.As(err, &status) || status.Status != http.StatusForbidden || status.Endpoint != "installation/repositories" {
			t.Errorf("err = %v, want *StatusError{installation/repositories, 403}", err)
		}
		if err != nil && strings.Contains(err.Error(), tokenValue(1)) {
			t.Errorf("error %q holds the token", err)
		}
	})
}
