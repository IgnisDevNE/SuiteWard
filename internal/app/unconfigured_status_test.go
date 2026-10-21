package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
)

func TestStatusNamesAnEnrolledRepositoryThatIsNotConfigured(t *testing.T) {
	for name, test := range map[string]struct {
		repos []string
		want  []string
	}{
		"configured":             {[]string{"acme/widgets"}, nil},
		"not configured":         {[]string{"acme/other"}, []string{"acme/widgets"}},
		"GitHub is not in use":   {nil, nil},
		"configured among other": {[]string{"acme/other", "acme/widgets"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			a := &api{owners: enrolledStore(t, "isolated"), logger: quietLogger(), tier: "isolated", store: fakeStore{version: 12}}
			if test.repos != nil {
				a.github = &githubStatus{repos: test.repos, store: githubsynctest.NewMemory(), ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}, merges: &bootstrapProblems{}, integrity: noVerdicts()}
			}

			code, body := call(t, a, http.MethodGet, "/status")

			var status struct {
				Unconfigured []string `json:"unconfiguredRepositories"`
			}
			if err := json.Unmarshal([]byte(body), &status); err != nil || code != http.StatusOK {
				t.Fatalf("GET /status = %d %s (%v); want 200 JSON", code, body, err)
			}
			if !slices.Equal(status.Unconfigured, test.want) {
				t.Fatalf("GET /status unconfiguredRepositories = %v; want %v\n%s", status.Unconfigured, test.want, body)
			}
		})
	}
}

func TestMCPStatusNamesAnEnrolledRepositoryThatIsNotConfigured(t *testing.T) {
	for name, test := range map[string]struct {
		repos []string
		want  []string
	}{
		"configured":     {[]string{"acme/widgets"}, nil},
		"not configured": {[]string{"acme/other"}, []string{"acme/widgets"}},
		"no GitHub":      {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			b := testBackend(githubsynctest.NewMemory(), &fakeInserter{}, &logs, test.repos...)
			b.owners, b.tier = enrolledStore(t, "isolated"), "isolated"

			view, err := b.Status(t.Context())

			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(view.UnconfiguredRepositories, test.want) {
				t.Fatalf("Status unconfiguredRepositories = %v; want %v", view.UnconfiguredRepositories, test.want)
			}
		})
	}
}
