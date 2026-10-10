package main

import (
	"strings"
	"testing"
)

var (
	entryX = treeEntry{Path: "tests/a_test.go", Type: "blob", SHA: "s1"}
	entryY = treeEntry{Path: "tests/a_test.go", Type: "blob", SHA: "s2"}
)

func TestApprovalComments(t *testing.T) {
	ref := refOf(entryX)
	cases := []struct {
		name       string
		login      string
		body       string
		conclusion string // "" means in_progress
		approved   bool
		reason     string // expected in the approval event, "" means none
	}{
		{"approved", "magalz", "/suiteward approve " + ref, "success", true, ""},
		{"approved on the first line only", "magalz", "/suiteward approve " + ref + "\nthanks", "success", true, ""},
		{"extra spaces", "magalz", "  /suiteward   approve  " + ref + "  ", "success", true, ""},
		{"wrong author", "mallory", "/suiteward approve " + ref, "failure", false, "wrong_author"},
		{"stale ref", "magalz", "/suiteward approve 000000000000", "failure", false, "stale_ref"},
		{"malformed: no ref", "magalz", "/suiteward approve", "failure", false, "malformed"},
		{"malformed: unknown verb", "magalz", "/suiteward ship " + ref, "failure", false, "malformed"},
		{"malformed: extra words", "magalz", "/suiteward approve " + ref + " please", "failure", false, "malformed"},
		{"not a command", "magalz", "looks good to me", "", false, ""},
		{"quoted first line", "magalz", "> /suiteward approve " + ref, "", false, ""},
		{"different-case login", "Magalz", "/suiteward approve " + ref, "failure", false, "wrong_author"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.gh.trees["aaaa"] = treeJSON(entryX)
			h.gh.comments = []fakeComment{{id: 7, login: tc.login, body: tc.body, updatedAt: "2026-01-01T00:00:00Z"}}
			h.cycle()
			got := h.lastCheck()
			if tc.conclusion == "" && strings.Contains(got, "conclusion") || tc.conclusion != "" && !strings.Contains(got, `"conclusion":"`+tc.conclusion+`"`) {
				t.Errorf("check = %s, want conclusion %q", got, tc.conclusion)
			}
			if gotApproved := h.a.st.Pulls[5].ApprovedCommentID == 7; gotApproved != tc.approved {
				t.Errorf("approved = %v, want %v", gotApproved, tc.approved)
			}
			hasReason := strings.Contains(h.out.String(), `"reason":"`+tc.reason+`"`)
			if tc.reason != "" && !hasReason || tc.reason == "" && strings.Contains(h.out.String(), `"reason"`) {
				t.Errorf("reason %q: log\n%s", tc.reason, h.out.String())
			}
			if tc.reason == "" && !tc.approved && strings.Contains(h.out.String(), `"event":"approval"`) {
				t.Errorf("an ordinary comment produced an approval event:\n%s", h.out.String())
			}
			// Rescans on 304 cycles must neither republish nor log the same verdict again.
			n, logged := len(h.gh.checks), strings.Count(h.out.String(), `"event":"approval"`)
			h.cycle()
			if len(h.gh.checks) != n || strings.Count(h.out.String(), `"event":"approval"`) != logged {
				t.Errorf("second cycle republished or logged again: checks %d -> %d, approval events %d -> %d", n, len(h.gh.checks), logged, strings.Count(h.out.String(), `"event":"approval"`))
			}
		})
	}
}

// An edit or a delete of the approving comment is observed once and logged; the approval stays.
func TestApprovingCommentEditAndDeleteAreObserved(t *testing.T) {
	h := newHarness(t)
	h.gh.trees["aaaa"] = treeJSON(entryX)
	c := fakeComment{id: 7, login: "magalz", body: "/suiteward approve " + refOf(entryX), updatedAt: "2026-01-01T00:00:00Z"}
	h.gh.comments = []fakeComment{c}
	h.cycle()
	checks := len(h.gh.checks)

	c.body, c.updatedAt = "never mind", "2026-01-02T00:00:00Z"
	h.gh.comments = []fakeComment{c}
	h.cycle()
	h.cycle()
	if n := strings.Count(h.out.String(), `"result":"edited"`); n != 1 {
		t.Fatalf("edited observed %d times, want 1:\n%s", n, h.out.String())
	}
	ps := h.a.st.Pulls[5]
	if ps.ApprovedCommentID != 7 || len(h.gh.checks) != checks {
		t.Fatalf("edit changed the approval: id %d, checks %q", ps.ApprovedCommentID, h.gh.checks)
	}

	h.gh.comments = nil
	h.cycle()
	h.cycle()
	if n := strings.Count(h.out.String(), `"result":"deleted"`); n != 1 {
		t.Fatalf("deleted observed %d times, want 1:\n%s", n, h.out.String())
	}
	if ps.ApprovedCommentID != 7 || len(h.gh.checks) != checks {
		t.Fatalf("delete changed the approval: id %d, checks %q", ps.ApprovedCommentID, h.gh.checks)
	}
}

// A push changes the ref, so the approval stops covering it; the old comment is not a failure.
// Reverting the push returns to the approved ref and the same comment approves again.
func TestApprovalFollowsTheRef(t *testing.T) {
	h := newHarness(t)
	h.gh.trees["aaaa"], h.gh.trees["bbbb"], h.gh.trees["cccc"] = treeJSON(entryX), treeJSON(entryY), treeJSON(entryX)
	h.gh.comments = []fakeComment{{id: 7, login: "magalz", body: "/suiteward approve " + refOf(entryX), updatedAt: "2026-01-01T00:00:00Z"}}
	h.cycle()
	if got := h.lastCheck(); !strings.Contains(got, `"conclusion":"success"`) {
		t.Fatalf("approved head: %s", got)
	}

	h.gh.head = "bbbb"
	h.cycle()
	if got := h.lastCheck(); !strings.Contains(got, `"head_sha":"bbbb"`) || strings.Contains(got, "conclusion") {
		t.Fatalf("pushed head must be in_progress, got %s", got)
	}

	h.gh.head = "cccc"
	h.cycle()
	if got := h.lastCheck(); !strings.Contains(got, `"head_sha":"cccc"`) || !strings.Contains(got, `"conclusion":"success"`) {
		t.Fatalf("reverted head must be approved again, got %s", got)
	}
}

// Two valid approvals of the current ref: the earliest comment in the list is the one recorded.
func TestEarliestValidApprovalWins(t *testing.T) {
	h := newHarness(t)
	h.gh.trees["aaaa"] = treeJSON(entryX)
	body := "/suiteward approve " + refOf(entryX)
	h.gh.comments = []fakeComment{
		{id: 7, login: "magalz", body: body, updatedAt: "2026-01-01T00:00:00Z"},
		{id: 8, login: "magalz", body: body, updatedAt: "2026-01-02T00:00:00Z"},
	}
	h.cycle()
	if got := h.a.st.Pulls[5].ApprovedCommentID; got != 7 {
		t.Fatalf("approved comment %d, want 7", got)
	}
}

// The owner login is compared as a plain, case-sensitive string, so a bot login works and a case variant does not.
func TestOwnerLoginIsAnExactMatch(t *testing.T) {
	for _, tc := range []struct {
		login string
		want  string
	}{{"suitewardq-spike[bot]", "success"}, {"SuiteWardQ-Spike[bot]", "failure"}} {
		h := newHarness(t)
		h.a.cfg.ownerLogin = "suitewardq-spike[bot]"
		h.gh.trees["aaaa"] = treeJSON(entryX)
		h.gh.comments = []fakeComment{{id: 7, login: tc.login, body: "/suiteward approve " + refOf(entryX), updatedAt: "2026-01-01T00:00:00Z"}}
		h.cycle()
		if got := h.lastCheck(); !strings.Contains(got, `"conclusion":"`+tc.want+`"`) {
			t.Errorf("login %q: check %s, want %s", tc.login, got, tc.want)
		}
	}
}
