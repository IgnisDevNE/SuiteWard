package main

import (
	"fmt"
	"strings"
	"testing"
)

func commitJSON(sha, tree, message string, parents ...string) string {
	var ps []string
	for _, p := range parents {
		ps = append(ps, fmt.Sprintf(`{"sha":%q}`, p))
	}
	return fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q},"parents":[%s],"message":%q}`, sha, tree, strings.Join(ps, ","), message)
}

func prJSON(merged bool, mergeSHA string) string {
	return fmt.Sprintf(`{"number":5,"state":"closed","merged":%v,"merge_commit_sha":%q,"commits":1,"merged_at":"2026-01-03T00:00:00Z","merged_by":{"login":"magalz"},"head":{"sha":"aaaa"},"base":{"ref":"main","sha":"base0"}}`, merged, mergeSHA)
}

// One merge scenario per GitHub method plus the odd cases, against recorded-shape fakes: the PR is approved
// while open, then closes, and the integrated tree decides whether a promotion is recorded.
func TestMergeDetection(t *testing.T) {
	const headMsg = "Add tests"
	cases := []struct {
		name         string
		approve      bool
		merged       bool
		mergeSHA     string
		mergeCommit  string
		mergeTree    []treeEntry
		method       string
		wantLog      []string
		wantMatch    bool
		wantPromoted bool
	}{
		{"merge commit", true, true, "mmmm", commitJSON("mmmm", "tree-m", "Merge pull request #5 from o/b", "base0", "aaaa"), []treeEntry{entryX},
			"merge", []string{`"method":"merge"`}, true, true},
		{"squash", true, true, "mmmm", commitJSON("mmmm", "tree-h", headMsg+" (#5)", "base0"), []treeEntry{entryX},
			"squash_or_rebase", []string{`"title_hint":"squash"`}, true, true},
		{"rebase", true, true, "mmmm", commitJSON("mmmm", "tree-h", headMsg, "base0"), []treeEntry{entryX},
			"squash_or_rebase", []string{`"title_hint":"rebase"`}, true, true},
		{"merge commit is the head", true, true, "aaaa", commitJSON("aaaa", "tree-h", headMsg, "base0"), []treeEntry{entryX},
			"squash_or_rebase", []string{`"merge_commit_is_head":true`, `"tree_equals_head":true`}, true, true},
		{"integrated tree differs from the approved one", true, true, "mmmm", commitJSON("mmmm", "tree-m", headMsg, "base0"), []treeEntry{entryY},
			"squash_or_rebase", []string{`"tree_equals_head":false`}, false, false},
		{"merged without approval", false, true, "mmmm", commitJSON("mmmm", "tree-m", headMsg, "base0"), []treeEntry{entryX},
			"squash_or_rebase", []string{`"reason":"no approval recorded"`}, false, false},
		{"closed without merging", true, false, "", "", nil, "", []string{`"merged":false`}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.gh.trees["aaaa"] = treeJSON(entryX)
			if tc.approve {
				h.gh.comments = []fakeComment{{id: 7, login: "magalz", body: "/suiteward approve " + refOf(entryX), updatedAt: "2026-01-01T00:00:00Z"}}
			}
			h.cycle()

			h.gh.open = false
			h.gh.prJSON = prJSON(tc.merged, tc.mergeSHA)
			h.gh.commits = map[string]string{"aaaa": commitJSON("aaaa", "tree-h", headMsg, "base0")}
			if tc.mergeCommit != "" {
				h.gh.commits[tc.mergeSHA] = tc.mergeCommit
				h.gh.trees["tree-m"] = treeJSON(tc.mergeTree...)
				h.gh.trees["tree-h"] = treeJSON(entryX)
			}
			h.cycle()

			m := h.a.st.Pulls[5].Merged
			if m.Method != tc.method || m.MatchesApproved != tc.wantMatch {
				t.Errorf("merged = %+v, want method %q matches %v", m, tc.method, tc.wantMatch)
			}
			if len(h.a.st.Promotions) != btoi(tc.wantPromoted) {
				t.Fatalf("promotions = %s", h.a.st.Promotions)
			}
			if tc.wantPromoted {
				if string(h.a.st.Promotions[0]) == "" || !strings.Contains(string(h.a.st.Promotions[0]), `"merge_commit_sha":"`+tc.mergeSHA+`"`) || m.IntegratedDigest != digestOf(tc.mergeTree...) {
					t.Errorf("promotion %s, integrated digest %q", h.a.st.Promotions[0], m.IntegratedDigest)
				}
			}
			for _, want := range tc.wantLog {
				if !strings.Contains(h.out.String(), want) {
					t.Errorf("log lacks %s:\n%s", want, h.out.String())
				}
			}

			// The outcome is final: no more requests but the PR list, and no second promotion.
			h.cycle()
			if got := h.a.c.calls; len(got) != 1 || len(h.a.st.Promotions) != btoi(tc.wantPromoted) {
				t.Errorf("third cycle calls %+v, promotions %s", got, h.a.st.Promotions)
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Expectation pending live runs (not an observed fact): GitHub may report base.sha equal to head.sha on a merged PR.
// If it does, the program records it as reported and does not interpret it.
func TestMergeRecordsHeadEqualToBase(t *testing.T) {
	h := newHarness(t)
	h.gh.trees["aaaa"] = treeJSON(entryX)
	h.gh.comments = []fakeComment{{id: 7, login: "magalz", body: "/suiteward approve " + refOf(entryX), updatedAt: "2026-01-01T00:00:00Z"}}
	h.cycle()

	h.gh.open = false
	h.gh.prJSON = strings.Replace(prJSON(true, "aaaa"), `"base":{"ref":"main","sha":"base0"}`, `"base":{"ref":"main","sha":"aaaa"}`, 1)
	h.gh.commits = map[string]string{"aaaa": commitJSON("aaaa", "tree-h", "Add tests", "base0")}
	h.gh.trees["tree-h"] = treeJSON(entryX)
	h.cycle()
	if !strings.Contains(h.out.String(), `"head_is_base":true`) || !h.a.st.Pulls[5].Merged.HeadIsBase {
		t.Fatalf("head_is_base not recorded:\n%s", h.out.String())
	}
}
