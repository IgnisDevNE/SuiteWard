package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	pullURI   = "/repos/octo-org/octo-repo/pulls/"
	closedURI = "/repos/octo-org/octo-repo/pulls?state=closed&sort=updated&direction=desc&per_page=30"

	mergeSHA = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b"
)

var mergedAt = time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC)

// pullFixture returns a single-pull fixture changed by change.
func pullFixture(t *testing.T, name string, change func(p map[string]any)) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(fixture(t, name), &p); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	change(p)
	return encode(t, p)
}

func samePullDetail(a, b github.PullDetail) bool {
	return samePull(a.Pull, b.Pull) && a.State == b.State && a.Merged == b.Merged &&
		a.MergedAt.Equal(b.MergedAt) && a.MergeCommitSHA == b.MergeCommitSHA
}

func TestPullReadsTheFactsOfOnePull(t *testing.T) {
	cases := map[string]struct {
		fixture string
		number  int
		want    github.PullDetail
	}{
		"open, carrying a test merge commit": {"pull_open.json", 42, github.PullDetail{State: "open"}},
		"closed and unmerged":                {"pull_closed_unmerged.json", 50, github.PullDetail{State: "closed"}},
		"merged with a merge commit": {"pull_merged.json", 51, github.PullDetail{
			State: "closed", Merged: true, MergedAt: mergedAt, MergeCommitSHA: mergeSHA,
		}},
		"merged without a merge commit yet": {"pull_merged_no_sha.json", 52, github.PullDetail{
			State: "closed", Merged: true, MergedAt: mergedAt,
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			uri := pullURI + strconv.Itoa(c.number)
			reader, gh := newReader(t, map[string]reply{uri: ok(fixture(t, c.fixture))})

			got, err := reader.Pull(t.Context(), repo, c.number)
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			want := c.want
			want.Pull = github.Pull{
				Number: c.number, HeadSHA: "1f0e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", HeadRef: "feature-" + strconv.Itoa(c.number),
				HeadRepo: repo, BaseRef: "main", BaseSHA: "a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4",
				UpdatedAt: time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC),
			}
			if !samePullDetail(got, want) {
				t.Errorf("Pull = %+v, want %+v", got, want)
			}
			if got := gh.uris(); !slices.Equal(got, []string{uri}) {
				t.Errorf("requests = %v, want one read of the pull", got)
			}
		})
	}
}

func TestPullHeadRepository(t *testing.T) {
	cases := map[string]struct {
		fixture string
		number  int
		want    string
	}{
		"the same repository": {"pull_open.json", 42, repo},
		"a fork":              {"pull_fork.json", 53, "octocat/octo-repo"},
		"a deleted head repo": {"pull_deleted_head.json", 54, "(deleted)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{pullURI + strconv.Itoa(c.number): ok(fixture(t, c.fixture))})
			got, err := reader.Pull(t.Context(), repo, c.number)
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			if got.HeadRepo != c.want {
				t.Errorf("HeadRepo = %q, want %q", got.HeadRepo, c.want)
			}
		})
	}
}

func TestPullDoesNotUseTheETagCache(t *testing.T) {
	const etag = `W/"pull-51"`
	reader, gh := newReader(t, map[string]reply{pullURI + "51": {body: fixture(t, "pull_merged.json"), etag: etag}})

	for range 2 {
		if _, err := reader.Pull(t.Context(), repo, 51); err != nil {
			t.Fatalf("Pull: %v", err)
		}
	}
	got := gh.requests()
	if len(got) != 2 {
		t.Fatalf("requests = %+v, want two full reads", got)
	}
	for i, r := range got {
		if r.ifNoneMatch != "" {
			t.Errorf("request %d sent If-None-Match %q, want none", i, r.ifNoneMatch)
		}
	}
}

func TestPullIgnoresTheTestMergeCommitOfAnUnmergedPull(t *testing.T) {
	// GitHub fills merge_commit_sha on an open pull; it must not look integrated.
	reader, _ := newReader(t, map[string]reply{pullURI + "42": ok(fixture(t, "pull_open.json"))})
	got, err := reader.Pull(t.Context(), repo, 42)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got.Merged || got.MergeCommitSHA != "" || !got.MergedAt.IsZero() {
		t.Errorf("Pull = %+v, want not merged and no merge commit", got)
	}
}

func TestPullRejectsAnInconsistentPull(t *testing.T) {
	cases := map[string]struct {
		fixture string
		number  int
		change  func(p map[string]any)
	}{
		"merged with a null merged_at":    {"pull_merged.json", 51, func(p map[string]any) { p["merged_at"] = nil }},
		"merged with a missing merged_at": {"pull_merged.json", 51, func(p map[string]any) { delete(p, "merged_at") }},
		"merged with a zero merged_at": {"pull_merged.json", 51, func(p map[string]any) {
			p["merged_at"] = "0001-01-01T00:00:00Z"
		}},
		"unmerged with a merged_at": {"pull_closed_unmerged.json", 50, func(p map[string]any) {
			p["merged_at"] = "2026-10-05T10:30:00Z"
		}},
		"merged and open":            {"pull_merged.json", 51, func(p map[string]any) { p["state"] = "open" }},
		"unknown state":              {"pull_open.json", 42, func(p map[string]any) { p["state"] = "draft" }},
		"missing state":              {"pull_open.json", 42, func(p map[string]any) { delete(p, "state") }},
		"missing merged":             {"pull_open.json", 42, func(p map[string]any) { delete(p, "merged") }},
		"short merge commit SHA":     {"pull_merged.json", 51, func(p map[string]any) { p["merge_commit_sha"] = "9c8b7a6f" }},
		"uppercase merge commit SHA": {"pull_merged.json", 51, func(p map[string]any) { p["merge_commit_sha"] = strings.ToUpper(mergeSHA) }},
		"another number":             {"pull_open.json", 42, func(p map[string]any) { p["number"] = 43 }},
		"head repo without a slash": {"pull_open.json", 42, func(p map[string]any) {
			object(object(p, "head"), "repo")["full_name"] = "octo-repo"
		}},
		"head repo without a name": {"pull_open.json", 42, func(p map[string]any) {
			delete(object(object(p, "head"), "repo"), "full_name")
		}},
		"short head SHA":     {"pull_open.json", 42, func(p map[string]any) { object(p, "head")["sha"] = "1f0e2d3c" }},
		"empty base ref":     {"pull_open.json", 42, func(p map[string]any) { object(p, "base")["ref"] = "" }},
		"missing updated_at": {"pull_open.json", 42, func(p map[string]any) { delete(p, "updated_at") }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{pullURI + strconv.Itoa(c.number): ok(pullFixture(t, c.fixture, c.change))})
			got, err := reader.Pull(t.Context(), repo, c.number)
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("Pull = %+v, %v; want ErrInvalidResponse", got, err)
			}
		})
	}
}

func TestPullNotFoundAndInvalidArguments(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{
		pullURI + "9": {status: http.StatusNotFound, body: fixture(t, "not_found.json")},
	})
	if _, err := reader.Pull(t.Context(), repo, 9); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("Pull error = %v, want rest.ErrNotFound", err)
	}
	for _, number := range []int{0, -1} {
		if _, err := reader.Pull(t.Context(), repo, number); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Pull(number %d) = %v, want ErrInvalidArgument", number, err)
		}
	}
	if _, err := reader.Pull(t.Context(), "octo-org", 42); !errors.Is(err, github.ErrInvalidArgument) {
		t.Errorf("Pull(bad repo) = %v, want ErrInvalidArgument", err)
	}
	if got := gh.uris(); !slices.Equal(got, []string{pullURI + "9"}) {
		t.Errorf("requests = %v, want only the read of pull 9", got)
	}
}

func TestClosedPullsReadsOnePageAndDerivesMerged(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{closedURI: ok(fixture(t, "closed_pulls.json"))})

	got, err := reader.ClosedPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("ClosedPulls: %v", err)
	}
	type fact struct {
		number   int
		state    string
		merged   bool
		mergedAt time.Time
		sha      string
		headRepo string
	}
	var facts []fact
	for _, p := range got {
		facts = append(facts, fact{p.Number, p.State, p.Merged, p.MergedAt, p.MergeCommitSHA, p.HeadRepo})
	}
	want := []fact{
		{51, "closed", true, mergedAt, mergeSHA, repo},
		{50, "closed", false, time.Time{}, "", repo},
		{54, "closed", true, mergedAt, mergeSHA, "(deleted)"},
	}
	if !slices.EqualFunc(facts, want, func(a, b fact) bool {
		return a.number == b.number && a.state == b.state && a.merged == b.merged && a.mergedAt.Equal(b.mergedAt) &&
			a.sha == b.sha && a.headRepo == b.headRepo
	}) {
		t.Errorf("ClosedPulls = %+v, want %+v (in GitHub's order)", facts, want)
	}
	if got := gh.uris(); !slices.Equal(got, []string{closedURI}) {
		t.Errorf("requests = %v, want one page with the closed query", got)
	}
}

func TestClosedPullsReadsOnlyOnePageEvenWhenFull(t *testing.T) {
	page := make([]map[string]any, 30)
	for i := range page {
		page[i] = items(t, "closed_pulls.json")[1]
		page[i]["number"] = 100 + i
	}
	reader, gh := newReader(t, map[string]reply{closedURI: ok(encode(t, page))})

	got, err := reader.ClosedPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("ClosedPulls: %v", err)
	}
	if len(got) != 30 || len(gh.uris()) != 1 {
		t.Errorf("ClosedPulls returned %d pulls in %d requests, want 30 in one", len(got), len(gh.uris()))
	}
}

func TestClosedPullsEmptyIsAnEmptySlice(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{closedURI: ok([]byte("[]\n"))})
	got, err := reader.ClosedPulls(t.Context(), repo)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ClosedPulls = %#v, %v; want an empty, non-nil slice", got, err)
	}
}

func TestClosedPullsNotModifiedServesTheCachedListing(t *testing.T) {
	const etag = `W/"closed-pulls"`
	reader, gh := newReader(t, map[string]reply{closedURI: {body: fixture(t, "closed_pulls.json"), etag: etag}})

	first, err := reader.ClosedPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("first ClosedPulls: %v", err)
	}
	second, err := reader.ClosedPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("second ClosedPulls: %v", err)
	}
	if len(second) != 3 || !slices.EqualFunc(second, first, samePullDetail) {
		t.Errorf("after a 304 ClosedPulls = %+v, want %+v", second, first)
	}
	if got := gh.requests(); len(got) != 2 || got[1].ifNoneMatch != etag {
		t.Errorf("requests = %+v, want a second, conditional request answered with 304", got)
	}
}

func TestClosedPullsRejectsAnInconsistentItem(t *testing.T) {
	cases := map[string]func(p map[string]any){
		"open state":                func(p map[string]any) { p["state"] = "open" },
		"unknown state":             func(p map[string]any) { p["state"] = "gone" },
		"short merge commit SHA":    func(p map[string]any) { p["merge_commit_sha"] = "9c8b" },
		"missing head SHA":          func(p map[string]any) { delete(object(p, "head"), "sha") },
		"head repo without a slash": func(p map[string]any) { object(object(p, "head"), "repo")["full_name"] = "octo-repo" },
		"zero number":               func(p map[string]any) { p["number"] = 0 },
		"zero merged_at":            func(p map[string]any) { p["merged_at"] = "0001-01-01T00:00:00Z" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{closedURI: ok(mutated(t, "closed_pulls.json", change))})
			got, err := reader.ClosedPulls(t.Context(), repo)
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("ClosedPulls = %+v, %v; want ErrInvalidResponse", got, err)
			}
		})
	}
	reader, _ := newReader(t, map[string]reply{closedURI: ok([]byte("null"))})
	if _, err := reader.ClosedPulls(t.Context(), repo); !errors.Is(err, github.ErrInvalidResponse) {
		t.Errorf("ClosedPulls(null) error = %v, want ErrInvalidResponse", err)
	}
	if _, err := reader.ClosedPulls(t.Context(), "octo-org"); !errors.Is(err, github.ErrInvalidArgument) {
		t.Errorf("ClosedPulls(bad repo) error = %v, want ErrInvalidArgument", err)
	}
}

func TestOpenPullsReadsTheHeadRepository(t *testing.T) {
	list := items(t, "pulls.json")
	object(list[1], "head")["repo"] = nil // #7 loses its repository, #42 keeps it
	reader, _ := newReader(t, map[string]reply{pullsURI + "1": ok(encode(t, list))})

	got, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	if len(got) != 2 || got[0].HeadRepo != "(deleted)" || got[1].HeadRepo != repo {
		t.Errorf("OpenPulls = %+v, want (deleted) and %q", got, repo)
	}
}

func TestMergeReadErrorsNeverCarryTheBody(t *testing.T) {
	const marker = "body-marker-27182"
	garbage := ok([]byte(marker + " <html>proxy error</html>"))
	reader, _ := newReader(t, map[string]reply{pullURI + "42": garbage, closedURI: garbage})

	_, pullErr := reader.Pull(t.Context(), repo, 42)
	_, closedErr := reader.ClosedPulls(t.Context(), repo)
	for what, err := range map[string]error{"Pull": pullErr, "ClosedPulls": closedErr} {
		switch {
		case !errors.Is(err, github.ErrInvalidResponse):
			t.Errorf("%s error = %v, want ErrInvalidResponse", what, err)
		case strings.Contains(err.Error(), marker):
			t.Errorf("%s error %q includes the body", what, err)
		}
	}
}

func TestMergeReadValidationErrorsNeverCarryGitHubText(t *testing.T) {
	const marker = "body-marker-16180"
	cases := map[string]func(p map[string]any){
		"unknown state":        func(p map[string]any) { p["state"] = marker },
		"bad head repository":  func(p map[string]any) { object(object(p, "head"), "repo")["full_name"] = marker },
		"bad merge commit SHA": func(p map[string]any) { p["merge_commit_sha"] = marker },
		"bad head SHA":         func(p map[string]any) { object(p, "head")["sha"] = marker },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			single := pullFixture(t, "pull_merged.json", change)
			listing := mutated(t, "closed_pulls.json", change)
			reader, _ := newReader(t, map[string]reply{pullURI + "51": ok(single), closedURI: ok(listing)})

			_, pullErr := reader.Pull(t.Context(), repo, 51)
			_, closedErr := reader.ClosedPulls(t.Context(), repo)
			for what, err := range map[string]error{"Pull": pullErr, "ClosedPulls": closedErr} {
				if !errors.Is(err, github.ErrInvalidResponse) || strings.Contains(err.Error(), marker) {
					t.Errorf("%s error = %v, want ErrInvalidResponse without the GitHub text", what, err)
				}
			}
		})
	}
}
