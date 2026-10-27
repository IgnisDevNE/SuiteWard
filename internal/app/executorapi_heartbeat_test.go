package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

func (e *execTestEnv) heartbeatExpiry(r execReply) time.Time {
	e.t.Helper()
	var body struct {
		LeaseExpiresAt string `json:"leaseExpiresAt"`
	}
	dec := json.NewDecoder(bytes.NewReader(r.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		e.t.Fatalf("heartbeat answer %s: %v", r.body, err)
	}
	at, err := time.Parse(time.RFC3339, body.LeaseExpiresAt)
	if err != nil || at.Location() != time.UTC {
		e.t.Fatalf("leaseExpiresAt %q: %v; want RFC 3339 in UTC", body.LeaseExpiresAt, err)
	}
	return at
}

func TestHeartbeatExtendsTheLeaseByAMinuteFromNow(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	e.clock.Advance(40 * time.Second) // the lease granted 60 s, 20 s are left

	got := e.send(http.MethodPost, e.jobPath(leased, "/heartbeat"), nil, withLease(leased.LeaseID))

	if got.status != http.StatusOK {
		t.Fatalf("heartbeat = %d %s; want 200", got.status, got.text())
	}
	if want := e.clock.Now().Add(execution.LeaseTTL); !e.heartbeatExpiry(got).Equal(want) {
		t.Errorf("leaseExpiresAt = %v; want %v, a minute from now", e.heartbeatExpiry(got), want)
	}
	if job := e.job(leased.ID); !job.LeaseExpiresAt.Equal(e.clock.Now().Add(execution.LeaseTTL)) {
		t.Errorf("stored lease expiry = %v; want the answer's", job.LeaseExpiresAt)
	}
}

func TestHeartbeatNeverExtendsPastTheDeadline(t *testing.T) {
	e := newExecEnv(t)
	e.times.JobTimeout = 90 * time.Second
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	e.clock.Advance(50 * time.Second)

	got := e.send(http.MethodPost, e.jobPath(leased, "/heartbeat"), nil, withLease(leased.LeaseID))

	if got.status != http.StatusOK || !e.heartbeatExpiry(got).Equal(leased.DeadlineAt) {
		t.Errorf("heartbeat = %d %s; want 200 with the lease expiring at the deadline %v", got.status, got.text(), leased.DeadlineAt)
	}
}

func TestHeartbeatAnswers409ForALostLease(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	path := e.jobPath(leased, "/heartbeat")
	other := "ffffffffffffffffffffffffffffffff"

	e.wantError(e.send(http.MethodPost, path, nil, withLease(other)), http.StatusConflict, "lease-lost")
	e.wantError(e.send(http.MethodPost, path, nil), http.StatusConflict, "lease-lost")
	e.wantError(e.send(http.MethodPost, path, nil, withLease("not a lease")), http.StatusConflict, "lease-lost")

	e.clock.Advance(execution.LeaseTTL) // the lease runs out
	e.wantError(e.send(http.MethodPost, path, nil, withLease(leased.LeaseID)), http.StatusConflict, "lease-lost")
}

func TestHeartbeatAnswers409ForAJobThatEnded(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	if _, err := e.store.Cancel(t.Context(), leased.ID, execution.ReasonClosed, e.clock.Now()); err != nil {
		t.Fatal(err)
	}

	e.wantError(e.send(http.MethodPost, e.jobPath(leased, "/heartbeat"), nil, withLease(leased.LeaseID)), http.StatusConflict, "lease-lost")
}

func TestHeartbeatAnswers404ForAnUnknownJob(t *testing.T) {
	e := newExecEnv(t)
	for _, path := range []string{"/jobs/99/heartbeat", "/jobs/0/heartbeat", "/jobs/-1/heartbeat", "/jobs/x/heartbeat", "/jobs/01/heartbeat", "/jobs/1.0/heartbeat", "/jobs/99999999999999999999/heartbeat"} {
		e.wantError(e.send(http.MethodPost, path, nil, withLease("ffffffffffffffffffffffffffffffff")), http.StatusNotFound, "not-found")
	}
}

// A heartbeat is the executor being seen, whatever the answer: it moves lastSeenAt and the last real request.
func TestHeartbeatCountsAsTheExecutorBeingSeen(t *testing.T) {
	for name, lease := range map[string]string{"right lease": "", "lost lease": "ffffffffffffffffffffffffffffffff"} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()
			if lease == "" {
				lease = leased.LeaseID
			}
			e.clock.Advance(10 * time.Second)

			e.send(http.MethodPost, e.jobPath(leased, "/heartbeat"), nil, withLease(lease))

			if at, requested := e.liveness.LastRequest(); !requested || !at.Equal(e.clock.Now()) {
				t.Errorf("last request = %v, %v; want %v", at, requested, e.clock.Now())
			}
			if !e.liveness.LastSeen().Equal(e.clock.Now()) {
				t.Errorf("lastSeen = %v; want %v", e.liveness.LastSeen(), e.clock.Now())
			}
		})
	}
}

// A request for a job that is not there counts too: the executor is alive, and its token says so.
func TestHeartbeatForAnUnknownJobCountsAsTheExecutorBeingSeen(t *testing.T) {
	e := newExecEnv(t)

	e.send(http.MethodPost, "/jobs/99/heartbeat", nil, withLease("ffffffffffffffffffffffffffffffff"))

	if _, requested := e.liveness.LastRequest(); !requested {
		t.Error("a heartbeat for an unknown job is not counted as an executor seen")
	}
}
