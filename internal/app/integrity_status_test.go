package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

// noVerdicts reads the integrity verdicts of an instance that has no enrollment, so /status shows none.
func noVerdicts() *integrityStatus {
	return &integrityStatus{owners: ownertest.NewMemory(), suites: governancetest.NewMemory(), store: integritytest.NewMemory()}
}

func TestStatusShowsTheIntegrityVerdictOfTheCurrentCanonicalOnly(t *testing.T) {
	for name, test := range map[string]struct {
		record *integrity.Record
		want   string
	}{
		"before the first verification": {nil, ""},
		"passed":                        {&integrity.Record{Version: "V1", Verdict: integrity.Passed}, "passed"},
		"failed":                        {&integrity.Record{Version: "V1", Verdict: integrity.Failed}, "failed"},
		"unavailable":                   {&integrity.Record{Version: "V1", Verdict: integrity.Unavailable}, "unavailable"},
		"a verdict of another version":  {&integrity.Record{Version: "V0", Verdict: integrity.Failed}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newIntWorld(t, true)
			if test.record != nil {
				r := *test.record
				r.Project, r.Suite, r.CheckedAt = w.project(), "default", w.clock
				if err := w.verdicts.Record(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			status := &githubStatus{repos: []string{bootRepo, bootOtherRepo}, store: w.pulls, ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: &bootstrapProblems{}, merges: &bootstrapProblems{},
				integrity: &integrityStatus{owners: w.owners, suites: w.suites, store: w.verdicts}}

			entries, err := status.report(t.Context(), nil)

			if err != nil {
				t.Fatal(err)
			}
			if entries[0].Integrity != test.want || entries[1].Integrity != "" { // the other repository has no Suite
				t.Fatalf("integrity %q and %q, want %q and none", entries[0].Integrity, entries[1].Integrity, test.want)
			}
			encoded, _ := json.Marshal(entries[0])
			if has := strings.Contains(string(encoded), `"integrity"`); has != (test.want != "") {
				t.Fatalf("status entry %s", encoded)
			}
		})
	}
}

// pullsServer answers the open pull listing and the comments of acme/widgets: pull 1 from the repository itself,
// pull 2 from a fork and pull 3 from a deleted fork.
func pullsServer(t *testing.T) *github.Reader {
	t.Helper()
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pull := func(number int, repo string) string {
		return `{"number":` + string(rune('0'+number)) + `,"head":{"ref":"feature","sha":"` + sha + `","repo":` + repo + `},"base":{"ref":"main","sha":"` + sha + `"},"updated_at":"2026-10-10T10:00:00Z"}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/widgets/pulls":
			_, _ = w.Write([]byte("[" + pull(1, `{"full_name":"acme/widgets"}`) + "," + pull(2, `{"full_name":"fork/widgets"}`) + "," + pull(3, `null`) + "]"))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rest.NewClient(func(context.Context) (string, string, error) { return "token", "identity", nil }, server.Client(), server.URL, budget)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func TestSynchronizationStoresTheHeadRepositoryOfEveryOpenPull(t *testing.T) {
	store := githubsynctest.NewMemory()
	reconciler, err := githubsync.NewReconciler(githubReader{pullsServer(t)}, store, time.Now, 42)
	if err != nil {
		t.Fatal(err)
	}

	if err := reconciler.SyncRepo(t.Context(), "acme/widgets"); err != nil {
		t.Fatal(err)
	}

	pulls, err := store.Pulls(t.Context(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, p := range pulls {
		got[p.Number] = p.HeadRepo
	}
	want := map[int]string{1: "acme/widgets", 2: "fork/widgets", 3: "(deleted)"}
	for number, headRepo := range want {
		if got[number] != headRepo {
			t.Fatalf("head repositories %v, want %v", got, want)
		}
	}
}
