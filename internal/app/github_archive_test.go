package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// archiveFakeGitHub is a GitHub that mints tokens, redirects the archive of a commit to a host of its own, serves it there, and says in its rate-limit
// headers that a window of 100 requests has used one more with each answer of the API.
type archiveFakeGitHub struct {
	mu        sync.Mutex
	tokenMaps []map[string]string
	served    int
	server    *httptest.Server
}

func (f *archiveFakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", setupInstallID):
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.tokenMaps = append(f.tokenMaps, body.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": body.Permissions})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/tarball/"+execSHA(1):
		f.served++
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(100-f.served))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.Header().Set("Location", f.server.URL+"/codeload/archive.tar.gz")
		w.WriteHeader(http.StatusFound)
	case r.Method == http.MethodGet && r.URL.Path == "/codeload/archive.tar.gz":
		_, _ = w.Write([]byte("the archive"))
	default:
		http.NotFound(w, r)
	}
}

// The archive reader is composed with the others: a token of its own that asks for the contents and nothing a pull request or a check could use, and
// the one budget of the process, which its request counts in.
func TestComposeGitHubBuildsTheArchiveReaderOnItsOwnTokenAndTheSharedBudget(t *testing.T) {
	fake := &archiveFakeGitHub{}
	fake.server = httptest.NewServer(fake)
	t.Cleanup(fake.server.Close)
	settings := config.GitHub{AppID: setupAppID, AppKey: config.Secret(setupKey()), InstallationID: setupInstallID, Repositories: []string{"acme/widgets"}, APIURL: fake.server.URL}

	gh, err := composeGitHub(settings, time.Minute, githubsynctest.NewMemory(), time.Now, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	if gh.archive == nil {
		t.Fatal("composeGitHub composed no archive reader")
	}
	if _, known := gh.budget.Window(); known {
		t.Fatal("the budget has a window before any request")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var got bytes.Buffer
	if n, err := gh.archive.Archive(ctx, "acme/widgets", execSHA(1), &got, 1<<20); err != nil || got.String() != "the archive" || n != int64(got.Len()) {
		t.Fatalf("Archive = %d, %v, %q; want the archive", n, err, got.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.tokenMaps) != 1 || len(fake.tokenMaps[0]) != 2 || fake.tokenMaps[0]["metadata"] != "read" || fake.tokenMaps[0]["contents"] != "read" {
		t.Errorf("tokens asked for %v; want one, with exactly metadata read and contents read", fake.tokenMaps)
	}
	if window, known := gh.budget.Window(); !known || window.Remaining != 49 {
		t.Errorf("budget window = %+v, known %v; want the archive request counted in the process-wide budget (49 of 50 left)", window, known)
	}
}
