package main

import (
	"strings"
	"testing"
)

// Against a fake GitHub: first cycle creates an in_progress check run, the second is all 304 and sends nothing,
// an approval comment turns it into a PATCH to success, and a new head sha gets a new check run.
func TestCycleCheckLifecycle(t *testing.T) {
	h := newHarness(t)
	x := treeEntry{Path: "tests/a_test.go", Type: "blob", SHA: "s1"}
	h.gh.trees["aaaa"], h.gh.trees["bbbb"] = treeJSON(x), treeJSON(x)

	h.cycle()
	if len(h.gh.checks) != 1 || !strings.HasPrefix(h.gh.checks[0], "POST ") || !strings.Contains(h.gh.checks[0], `"status":"in_progress"`) {
		t.Fatalf("first cycle: %q", h.gh.checks)
	}
	h.cycle()
	if len(h.gh.checks) != 1 {
		t.Fatalf("unchanged cycle published again: %q", h.gh.checks)
	}
	if got := h.a.c.calls; len(got) != 3 || got[0].Status != 304 || got[1].Status != 304 || got[2].Status != 304 {
		t.Fatalf("second cycle calls %+v, want three 304s (list, tree, comments)", got)
	}

	h.gh.comments = []fakeComment{{id: 1234, login: "magalz", body: "/suiteward approve " + refOf(x), updatedAt: "2026-01-01T00:00:00Z"}}
	h.cycle()
	if len(h.gh.checks) != 2 || !strings.HasPrefix(h.gh.checks[1], "PATCH ") || !strings.Contains(h.gh.checks[1], `"conclusion":"success"`) {
		t.Fatalf("approval cycle: %q", h.gh.checks)
	}

	h.gh.head = "bbbb"
	h.cycle()
	if len(h.gh.checks) != 3 || !strings.HasPrefix(h.gh.checks[2], "POST ") || !strings.Contains(h.gh.checks[2], `"head_sha":"bbbb"`) {
		t.Fatalf("new head cycle: %q", h.gh.checks)
	}
}
