//go:build live

package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// readPermissions is the map internal/app (composeGitHub) requests; keep them equal.
// A successful Token call proves GitHub granted exactly this map (appauth fails with
// ErrPermissionsMismatch otherwise).
var readPermissions = map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read"}

func liveEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required by -tags=live", name)
	}
	return v
}

func stepCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestLive runs the production GitHub adapter against the real App on a private
// repository. It logs only statuses, counts and the token identity, never a token,
// key or response body.
func TestLive(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	repo := liveEnv(t, "SUITEWARD_TEST_GITHUB_REPO")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatal("SUITEWARD_TEST_GITHUB_REPO must be owner/name")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	newClient := func(permissions map[string]string) (*appauth.Installation, *rest.Client) {
		inst, err := app.Installation(installationID, []string{name}, permissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		client, err := rest.NewClient(func(ctx context.Context) (string, string, error) {
			tok, err := inst.Token(ctx)
			if err != nil {
				return "", "", err
			}
			return tok.Reveal(), tok.Identity(), nil
		}, httpClient, apiURL, budget)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return inst, client
	}
	inst, client := newClient(readPermissions)
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var number int
	var sha string

	t.Run("token", func(t *testing.T) {
		tok, err := inst.Token(stepCtx(t))
		if err != nil {
			t.Fatalf("Token (also fails when granted permissions differ from %v): %s", readPermissions, failure(err))
		}
		t.Logf("token identity %s, expires %s", tok.Identity(), tok.ExpiresAt().UTC().Format(time.RFC3339))
	})

	t.Run("open pulls", func(t *testing.T) {
		pulls, err := reader.OpenPulls(stepCtx(t), repo)
		if err != nil {
			t.Fatalf("OpenPulls: %s", failure(err))
		}
		t.Logf("%d open pulls", len(pulls))
	})

	t.Run("pull by number", func(t *testing.T) {
		ctx := stepCtx(t)
		list := getJSON[[]struct {
			Number int `json:"number"`
		}](t, ctx, client, "/repos/"+repo+"/pulls?state=all&per_page=1")
		if len(list) == 0 {
			t.Fatalf("%s has no pull request; open one so the pull and comment reads can be checked", repo)
		}
		number = list[0].Number
		resp, err := client.Get(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number))
		if err != nil {
			t.Fatalf("GET pull %d: %s", number, failure(err))
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("GET pull %d answered %d, want 200", number, resp.Status)
		}
		t.Logf("GET pull %d answered %d", number, resp.Status)
	})

	t.Run("commit read", func(t *testing.T) {
		ctx := stepCtx(t)
		info := getJSON[struct {
			DefaultBranch string `json:"default_branch"`
		}](t, ctx, client, "/repos/"+repo)
		if info.DefaultBranch == "" {
			t.Fatal("the repository has no default_branch")
		}
		head := getJSON[struct {
			SHA string `json:"sha"`
		}](t, ctx, client, "/repos/"+repo+"/commits/"+info.DefaultBranch)
		if head.SHA == "" {
			t.Fatal("the default branch has no head commit sha")
		}
		sha = head.SHA
		resp, err := client.Get(ctx, "/repos/"+repo+"/commits/"+sha)
		if err != nil {
			t.Fatalf("GET commit %s: %s", sha, failure(err))
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("GET commit %s answered %d, want 200", sha, resp.Status)
		}
		t.Logf("GET commit %s answered %d", sha, resp.Status)
	})

	t.Run("tree", func(t *testing.T) {
		if sha == "" {
			t.Fatal("no commit sha: the commit read step failed")
		}
		ctx := stepCtx(t)
		tree, err := reader.Tree(ctx, repo, sha)
		if err != nil {
			t.Fatalf("Tree: %s", failure(err))
		}
		entries, err := tree.Entries(ctx)
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("the tree of the default branch head is empty")
		}
		t.Logf("tree has %d entries", len(entries))
	})

	t.Run("comments", func(t *testing.T) {
		if number == 0 {
			t.Fatal("no pull number: the pull by number step failed")
		}
		comments, err := reader.PullComments(stepCtx(t), repo, number, time.Time{})
		if err != nil {
			t.Fatalf("PullComments: %s", failure(err))
		}
		t.Logf("pull %d has %d comments", number, len(comments))
	})

	t.Run("narrowed negative check", func(t *testing.T) {
		if number == 0 || sha == "" {
			t.Fatal("no pull number or commit sha: an earlier step failed")
		}
		_, narrow := newClient(map[string]string{"metadata": "read"})
		for _, path := range []string{"/repos/" + repo + "/pulls/" + strconv.Itoa(number), "/repos/" + repo + "/commits/" + sha} {
			_, err := narrow.Get(stepCtx(t), path)
			var se *rest.StatusError
			if !errors.As(err, &se) || (se.Status != http.StatusForbidden && se.Status != http.StatusNotFound) {
				t.Errorf("metadata-only token on %s: want a 403 or 404 StatusError, got %s", path, failure(err))
				continue
			}
			t.Logf("metadata-only token on %s answered %d", path, se.Status)
		}
	})
}

func getJSON[T any](t *testing.T, ctx context.Context, client *rest.Client, path string) T {
	t.Helper()
	resp, err := client.Get(ctx, path)
	if err != nil {
		t.Fatalf("GET %s: %s", path, failure(err))
	}
	var v T
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		t.Fatalf("GET %s: the answer is not the expected JSON", path)
	}
	return v
}

func liveInt(t *testing.T, name string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(liveEnv(t, name), 10, 64)
	if err != nil {
		t.Fatalf("%s must be an integer", name)
	}
	return n
}

// failure renders an error without a GitHub answer body, which a StatusError carries.
func failure(err error) string {
	var se *rest.StatusError
	if errors.As(err, &se) {
		return "status " + strconv.Itoa(se.Status)
	}
	return err.Error()
}
