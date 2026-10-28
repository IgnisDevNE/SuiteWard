package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// An empty tree is a list in the lease answer, not null: the tree reader of the service never hands the handler a nil slice.
func TestGitHubTreesOfAnEmptyTreeIsAnEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":[],"truncated":false}`))
	}))
	t.Cleanup(server.Close)
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rest.NewClient(func(context.Context) (string, string, error) { return "t", "t", nil }, server.Client(), server.URL, budget)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := githubTrees(reader)(t.Context(), "acme/widgets", execSHA(1))

	if err != nil {
		t.Fatal(err)
	}
	if encoded, err := json.Marshal(leaseSide{SHA: execSHA(1), Tree: entries}); err != nil || string(encoded) != `{"sha":"`+execSHA(1)+`","tree":[]}` {
		t.Errorf("the lease side = %s, %v; want an empty list for the tree", encoded, err)
	}
}
