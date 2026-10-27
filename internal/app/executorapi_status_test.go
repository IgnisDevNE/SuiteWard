package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

type fakeJobCounts struct {
	counts map[execution.State]int
	err    error
}

func (f fakeJobCounts) Counts(context.Context) (map[execution.State]int, error) {
	return f.counts, f.err
}

func statusOf(t *testing.T, a *api) map[string]any {
	t.Helper()
	code, body := call(t, a, http.MethodGet, "/status")
	var status map[string]any
	if err := json.Unmarshal([]byte(body), &status); err != nil || code != http.StatusOK {
		t.Fatalf("GET /status = %d %s (%v); want 200 JSON", code, body, err)
	}
	return status
}

// The execution block states the token's presence, the jobs of every state (zero included) and when an executor was last seen: counts and a
// time, never a repository, a path or a test name.
func TestStatusReportsTheExecutionBlock(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	liveness := execution.NewLiveness(start)
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), store: fakeStore{version: 21}, executorStatus: &executorStatus{
		enabled: true, liveness: liveness, jobs: fakeJobCounts{counts: map[execution.State]int{execution.StatePending: 2, execution.StateUnavailable: 3, execution.StateComplete: 7}},
	}}

	got := statusOf(t, a)["execution"]

	want := `{"enabled":true,"lastSeenAt":"2026-10-27T09:30:00Z","jobs":{"pending":2,"leased":0,"complete":7,"unavailable":3,"cancelled":0}}`
	text, err := json.Marshal(got)
	if err != nil || !jsonEqual(t, string(text), want) {
		t.Fatalf("status.execution = %s; want %s", text, want)
	}
}

// An executor seen after the start moves lastSeenAt, and a service with no token says so while it still counts the jobs created
// already unavailable for executor-not-configured.
func TestStatusExecutionFollowsTheLivenessAndShowsAMissingToken(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	liveness := execution.NewLiveness(start)
	liveness.Saw(start.Add(90 * time.Second))
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), store: fakeStore{version: 21}, executorStatus: &executorStatus{
		liveness: liveness, jobs: fakeJobCounts{counts: map[execution.State]int{execution.StateUnavailable: 1}},
	}}

	block, ok := statusOf(t, a)["execution"].(map[string]any)

	if !ok || block["enabled"] != false || block["lastSeenAt"] != "2026-10-27T09:31:30Z" {
		t.Fatalf("status.execution = %v; want enabled false and lastSeenAt 2026-10-27T09:31:30Z", block)
	}
	if jobs, _ := block["jobs"].(map[string]any); jobs["unavailable"] != float64(1) {
		t.Errorf("status.execution.jobs = %v; want the unavailable job counted without a token", jobs)
	}
}

// A store that cannot count the jobs makes /status unavailable with a fixed reason, never the cause.
func TestStatusExecutionHidesTheCauseOfAFailedCount(t *testing.T) {
	a := &api{owners: ownertest.NewMemory(), logger: quietLogger(), store: fakeStore{version: 21}, executorStatus: &executorStatus{
		liveness: execution.NewLiveness(time.Now()), jobs: fakeJobCounts{err: errors.New("dial tcp db.internal:5432: password=hunter2")},
	}}

	code, body := call(t, a, http.MethodGet, "/status")

	if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
		t.Fatalf("GET /status = %d %s; want 503 with the fixed reason", code, body)
	}
}
