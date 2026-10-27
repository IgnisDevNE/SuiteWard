package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// treeEntriesOfFixture are the blobs and submodules of testdata/tree.json with their modes and git ids: its directories are left out.
var treeEntriesOfFixture = []github.TreeEntry{
	{Path: ".suiteward.yml", Mode: "100644", SHA: "d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0"},
	{Path: "README.md", Mode: "100644", SHA: "aa11bb22cc33dd44ee55ff6600aa11bb22cc33dd"},
	{Path: "docs/link", Mode: "120000", SHA: "5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e"},
	{Path: "scripts/run.sh", Mode: "100755", SHA: "77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa"},
	{Path: "tests/a_test.go", Mode: "100644", SHA: "b82e9a34624d830a4bd24b63ceb1deb0ad14c863"},
	{Path: "vendor/lib", Mode: "160000", SHA: "4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e"},
}

func TestTreeEntriesKeepsTheModeAndTheGitIdOfBlobsAndSubmodules(t *testing.T) {
	// The fake offers an ETag: the read is of an immutable path and must never send If-None-Match.
	reader, gh := newReader(t, map[string]reply{treeURI: {body: fixture(t, "tree.json"), etag: `"tree"`}})

	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	if err != nil {
		t.Fatalf("TreeEntries: %v", err)
	}
	if len(entries) != len(treeEntriesOfFixture) {
		t.Fatalf("TreeEntries = %+v; want the %d blobs and submodules", entries, len(treeEntriesOfFixture))
	}
	for i, want := range treeEntriesOfFixture {
		if entries[i] != want {
			t.Errorf("entry %d = %+v; want %+v", i, entries[i], want)
		}
	}
	got := gh.requests()
	if len(got) != 1 || got[0].uri != treeURI || got[0].ifNoneMatch != "" {
		t.Errorf("requests = %+v; want one read of %s without If-None-Match", got, treeURI)
	}
}

// 100664 is git's legacy mode of a regular file: Tree refuses it, and the executor's lease lists it.
func TestTreeEntriesAcceptsTheLegacyModeOfARegularFile(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok(treeWith("old/file.go", "100664", "blob", readmeBlob))})

	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	want := github.TreeEntry{Path: "old/file.go", Mode: "100664", SHA: readmeBlob}
	if err != nil || len(entries) != 1 || entries[0] != want {
		t.Fatalf("TreeEntries = %+v, %v; want %+v", entries, err, want)
	}
}

func TestTreeEntriesRejectsAnEntryThatGitDoesNotList(t *testing.T) {
	const sha = "77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa"
	for _, tc := range []struct{ name, path, mode, kind, sha string }{
		{"an unknown mode", "tests/odd_entry", "100600", "blob", sha},
		{"a symlink mode as a commit", "tests/odd_entry", "120000", "commit", sha},
		{"a submodule mode as a blob", "tests/odd_entry", "160000", "blob", sha},
		{"a file mode as a tree", "tests/odd_entry", "100644", "tree", sha},
		{"a directory mode as a blob", "tests/odd_entry", "040000", "blob", sha},
		{"an unknown type", "tests/odd_entry", "100644", "tag", sha},
		{"a git id that is not one", "tests/odd_entry", "100644", "blob", "../../../pulls"},
		{"an upper-case git id", "tests/odd_entry", "100644", "blob", strings.ToUpper(sha)},
		{"no path", "", "100644", "blob", sha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{treeURI: ok(treeWith(tc.path, tc.mode, tc.kind, tc.sha))})

			entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

			if !errors.Is(err, github.ErrInvalidResponse) || errors.Is(err, github.ErrTreeTruncated) || entries != nil {
				t.Fatalf("TreeEntries = %+v, %v; want ErrInvalidResponse only and no entries", entries, err)
			}
			if strings.Contains(err.Error(), "odd_entry") {
				t.Errorf("error %q quotes the path that GitHub sent", err)
			}
		})
	}
}

// A tree that GitHub cut is no list of entries: a partial list would let the executor run on part of the tree.
func TestTreeEntriesReturnsNoEntriesForATruncatedTree(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok(fixture(t, "tree_truncated.json"))})

	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	if !errors.Is(err, github.ErrTreeTruncated) || entries != nil {
		t.Fatalf("TreeEntries = %+v, %v; want ErrTreeTruncated and no entries", entries, err)
	}
}

func TestTreeEntriesRejectsAnAnswerWithoutItsList(t *testing.T) {
	for name, body := range map[string]string{
		"no tree key": `{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","truncated":false}`,
		"null tree":   `{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":null,"truncated":false}`,
		"null answer": `null`,
		"not JSON":    `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{treeURI: ok([]byte(body))})
			if entries, err := reader.TreeEntries(t.Context(), repo, commitSHA); !errors.Is(err, github.ErrInvalidResponse) || entries != nil {
				t.Fatalf("TreeEntries = %+v, %v; want ErrInvalidResponse", entries, err)
			}
		})
	}
}

func TestTreeEntriesOfAnEmptyTreeIsAnEmptyList(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok([]byte(`{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":[],"truncated":false}`))})

	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	if err != nil || entries == nil || len(entries) != 0 {
		t.Errorf("TreeEntries = %#v, %v; want an empty list, not nil", entries, err)
	}
}

func TestTreeEntriesRejectsABadRepositoryOrCommitBeforeAnyRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{})
	for _, bad := range []string{"", "octo-org", "octo-org/", "octo-org/octo-repo/extra", "octo-org/octo-repo?page=2", "../octo-repo", "octo-org/.."} {
		if _, err := reader.TreeEntries(t.Context(), bad, commitSHA); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("TreeEntries(%q) = %v; want ErrInvalidArgument", bad, err)
		}
	}
	for _, bad := range []string{"", "c0ffee", strings.ToUpper(commitSHA), commitSHA + "0", "HEAD", "main", "../" + commitSHA[3:]} {
		if _, err := reader.TreeEntries(t.Context(), repo, bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("TreeEntries(sha %q) = %v; want ErrInvalidArgument", bad, err)
		}
	}
	if got := gh.uris(); len(got) != 0 {
		t.Errorf("requests = %v; want none", got)
	}
}

func TestTreeEntriesKeepsANotFoundMatchable(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: {status: http.StatusNotFound, body: fixture(t, "not_found.json")}})

	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	if !errors.Is(err, rest.ErrNotFound) || entries != nil {
		t.Errorf("TreeEntries = %+v, %v; want rest.ErrNotFound", entries, err)
	}
}

// entriesAPI answers every tree read with the fixture and reports one more request used in a window of 10, as GitHub does.
type entriesAPI struct {
	*httptest.Server
	mu   sync.Mutex
	seen int
}

func startEntriesAPI(t *testing.T) *entriesAPI {
	t.Helper()
	api := &entriesAPI{}
	body := fixture(t, "tree.json")
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		api.mu.Lock()
		api.seen++
		remaining := 10 - api.seen
		api.mu.Unlock()
		w.Header().Set("X-RateLimit-Limit", "10")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		_, _ = w.Write(body)
	}))
	t.Cleanup(api.Close)
	return api
}

func (a *entriesAPI) requests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen
}

// The read counts once in the process-wide budget and honours its refusal, like every other read.
func TestTreeEntriesCountsAgainstTheBudgetAndHonoursItsRefusal(t *testing.T) {
	api := startEntriesAPI(t)
	budget, err := rest.NewBudget(0.2, time.Now) // 2 of a window of 10
	if err != nil {
		t.Fatal(err)
	}
	token := func(context.Context) (string, string, error) { return "fake-token", "fake-identity", nil }
	client, err := rest.NewClient(token, http.DefaultClient, api.URL, budget)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 2; i++ {
		if _, err := reader.TreeEntries(t.Context(), repo, commitSHA); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	entries, err := reader.TreeEntries(t.Context(), repo, commitSHA)

	if !errors.Is(err, rest.ErrBudgetExhausted) || entries != nil {
		t.Fatalf("third read = %+v, %v; want the budget's refusal", entries, err)
	}
	if n := api.requests(); n != 2 {
		t.Errorf("GitHub saw %d requests; want 2, the refusal calls nobody", n)
	}
}
