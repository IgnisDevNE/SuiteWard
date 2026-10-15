package app

import (
	"context"
	"crypto/sha1" //nolint:gosec // the fake GitHub names blobs the way git does
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// mergeAnswer is one answer of the GitHub the adapter tests serve.
type mergeAnswer struct {
	status int
	body   string
}

// realMergeReader returns the GitHub adapter over an HTTP server that answers the merged pull request 5 and then what
// treeAnswer says to the tree of its merge commit (blobs answer blobAnswer), so the job meets the adapter's real errors.
func realMergeReader(t *testing.T, treeAnswer, blobAnswer mergeAnswer) *github.Reader {
	t.Helper()
	pull := fmt.Sprintf(`{"number": 5, "state": "closed", "merged": true, "merged_at": %q, "merge_commit_sha": %q, "updated_at": %q,
		"head": {"ref": "feature", "sha": %q, "repo": {"full_name": %q}}, "base": {"ref": "main", "sha": %q}}`,
		bootTime.Add(2*time.Hour).Format(time.RFC3339), mgSHA5, bootTime.Add(2*time.Hour).Format(time.RFC3339), cjHead2, bootRepo, bootHead)
	var entries []string
	for path, content := range bootFiles("v2") {
		entries = append(entries, fmt.Sprintf(`{"path": %q, "mode": "100644", "type": "blob", "sha": "%x"}`, path, sha1.Sum([]byte(content)))) //nolint:gosec // see the import
	}
	answers := map[string]mergeAnswer{
		"/repos/" + bootRepo + "/pulls/5":             {http.StatusOK, pull},
		"/repos/" + bootRepo + "/pulls":               {http.StatusOK, "[]"},
		"/repos/" + bootRepo + "/git/trees/" + mgSHA5: treeAnswer,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, known := answers[r.URL.Path]
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/"+bootRepo+"/git/blobs/"):
			answer = blobAnswer
		case !known:
			answer = mergeAnswer{http.StatusNotFound, "{}"}
		}
		w.WriteHeader(answer.status)
		_, _ = w.Write([]byte(strings.ReplaceAll(answer.body, "ENTRIES", strings.Join(entries, ","))))
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	budget := bootMust(rest.NewBudget(0.5, time.Now))
	client := bootMust(rest.NewClient(func(context.Context) (string, string, error) { return "token", "installation", nil }, server.Client(), server.URL, budget))
	return bootMust(github.NewReader(client))
}

const (
	listedTree    = `{"tree": [ENTRIES], "truncated": false}`
	truncatedTree = `{"tree": [ENTRIES], "truncated": true}`
)

// TestMergesMeetTheRealAdapterErrors serves the answers GitHub gives for a merge commit that is not readable yet,
// through the real reader: they end as an unreadable integration after the bound, never as an invalid source, and a
// blob that cannot be read is a failed read.
func TestMergesMeetTheRealAdapterErrors(t *testing.T) {
	unreadable := map[string]mergeAnswer{
		"a truncated tree":     {http.StatusOK, truncatedTree},
		"a tree not found":     {http.StatusNotFound, fmt.Sprintf(`{"message": %q}`, bootSecret)},
		"a tree unprocessable": {http.StatusUnprocessableEntity, fmt.Sprintf(`{"message": %q}`, bootSecret)},
	}
	for name, answer := range unreadable {
		t.Run(name, func(t *testing.T) {
			w := newMgWorld(t)
			w.approved(5, cjHead2, "v2", 2, 201)
			w.leave(5)
			w.merges.reader = realMergeReader(t, answer, mergeAnswer{http.StatusOK, "{}"})

			w.advance(10 * time.Minute)
			w.mustMerge()
			w.wantNoOutcome(5)
			w.wantProblem("waiting")

			w.advance(20 * time.Minute)
			w.mustMerge()
			w.wantOutcome(5, governance.MergeBlocked, governance.MergeReasonIntegrationUnreadable)
			if strings.Contains(w.logs.String(), bootSecret) {
				t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
			}
		})
	}
	t.Run("a blob that cannot be read", func(t *testing.T) {
		w := newMgWorld(t)
		w.approved(5, cjHead2, "v2", 2, 201)
		w.leave(5)
		w.merges.reader = realMergeReader(t, mergeAnswer{http.StatusOK, listedTree}, mergeAnswer{http.StatusBadGateway, mustJSON(map[string]string{"message": bootSecret})})
		w.advance(2 * time.Hour)

		err := w.runMerges()

		if err != nil {
			t.Fatalf("a failed GitHub read failed the run: %v", err)
		}
		w.wantNoOutcome(5)
		w.wantProblem("read-failed")
		if strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
		}
	})
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
