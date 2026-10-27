package github_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	mergeBaseSHA  = "00112233445566778899aabbccddeeff00112233"
	mergeBaseHead = "0123456789abcdef0123456789abcdef01234567"
	mergeBaseTip  = "fedcba9876543210fedcba9876543210fedcba98"
	mergeBaseURI  = "/repos/octo-org/octo-repo/compare/" + mergeBaseTip + "..." + mergeBaseHead + "?per_page=1"
)

func mergeBaseAnswer(sha string) []byte {
	return []byte(`{"merge_base_commit":{"sha":"` + sha + `"},"status":"ahead","ahead_by":1,"commits":[]}`)
}

func TestMergeBaseReadsTheSHAOfTheMergeBaseCommitWithOneRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{mergeBaseURI: ok(mergeBaseAnswer(mergeBaseSHA))})

	sha, err := reader.MergeBase(t.Context(), repo, mergeBaseTip, mergeBaseHead)
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if sha != mergeBaseSHA {
		t.Errorf("merge base = %q, want %q", sha, mergeBaseSHA)
	}
	if got := gh.uris(); !slices.Equal(got, []string{mergeBaseURI}) {
		t.Errorf("requests = %v, want the comparison of the two commits with one commit listed", got)
	}
	if got := gh.requests()[0].ifNoneMatch; got != "" {
		t.Errorf("If-None-Match = %q: the answer for two SHAs is immutable and is read without the ETag cache", got)
	}
}

func TestMergeBaseReportsNotFoundWhenGitHubHasNoCommonAncestor(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{mergeBaseURI: {status: http.StatusNotFound, body: fixture(t, "not_found.json")}})

	_, err := reader.MergeBase(t.Context(), repo, mergeBaseTip, mergeBaseHead)

	if !errors.Is(err, rest.ErrNotFound) {
		t.Fatalf("MergeBase error = %v, want rest.ErrNotFound", err)
	}
}

func TestMergeBaseTreatsAnyOtherFailureAsNotNotFound(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{mergeBaseURI: {status: http.StatusBadGateway, body: []byte("down")}})

	_, err := reader.MergeBase(t.Context(), repo, mergeBaseTip, mergeBaseHead)

	if err == nil || errors.Is(err, rest.ErrNotFound) {
		t.Fatalf("MergeBase error = %v, want a failure that is not ErrNotFound", err)
	}
}

func TestMergeBaseRejectsAnAnswerWithoutAValidMergeBaseSHA(t *testing.T) {
	for name, sha := range map[string]string{
		"empty": "", "short": "c0ffee", "upper case": strings.ToUpper(mergeBaseSHA), "too long": mergeBaseSHA + "0", "not hex": "g" + mergeBaseSHA[1:],
	} {
		reader, _ := newReader(t, map[string]reply{mergeBaseURI: ok(mergeBaseAnswer(sha))})
		if _, err := reader.MergeBase(t.Context(), repo, mergeBaseTip, mergeBaseHead); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", name, err)
		}
	}
	for name, answer := range map[string]string{"no merge base": `{"status":"ahead"}`, "not json": `<html>`} {
		reader, _ := newReader(t, map[string]reply{mergeBaseURI: ok([]byte(answer))})
		if _, err := reader.MergeBase(t.Context(), repo, mergeBaseTip, mergeBaseHead); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", name, err)
		}
	}
}

func TestMergeBaseSendsNoRequestForAMalformedArgument(t *testing.T) {
	cases := map[string]struct{ repo, base, head string }{
		"repository":      {"not-a-repo", mergeBaseTip, mergeBaseHead},
		"base":            {repo, "main", mergeBaseHead},
		"head":            {repo, mergeBaseTip, strings.ToUpper(mergeBaseHead)},
		"empty base":      {repo, "", mergeBaseHead},
		"base with range": {repo, mergeBaseTip + "..." + mergeBaseHead, mergeBaseHead},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reader, gh := newReader(t, map[string]reply{})

			_, err := reader.MergeBase(t.Context(), c.repo, c.base, c.head)

			if !errors.Is(err, github.ErrInvalidArgument) {
				t.Errorf("err = %v, want ErrInvalidArgument", err)
			}
			if uris := gh.uris(); len(uris) != 0 {
				t.Errorf("requests = %v, want none", uris)
			}
		})
	}
}
