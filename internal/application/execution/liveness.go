package execution

import (
	"sync"
	"time"
)

// Liveness is the service's in-memory record of when an executor was last seen, from its lease requests (answered with a job, 204 or
// 502) and its heartbeats (docs/contracts/runtime.md, "The executor is seen, not asked"). It keeps nothing across a restart, and
// it never moves back: a time earlier than the one held is ignored, so concurrent handlers cannot make an executor look older.
type Liveness struct {
	mu          sync.Mutex
	lastSeen    time.Time
	lastRequest time.Time
	requested   bool
}

// NewLiveness starts the record at start: the service has just started, so it does not see a missing executor at once.
func NewLiveness(start time.Time) *Liveness { return &Liveness{lastSeen: start} }

// Saw records that a lease request or a heartbeat arrived at at.
func (l *Liveness) Saw(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at.After(l.lastSeen) {
		l.lastSeen = at
	}
	if !l.requested || at.After(l.lastRequest) {
		l.lastRequest, l.requested = at, true
	}
}

// LastSeen is the time an executor was last seen: the start time until the first Saw. It drives the no-executor timeout.
func (l *Liveness) LastSeen() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSeen
}

// LastRequest is the time of the last lease request or heartbeat that arrived, false until the first Saw: a restart of the service
// alone never counts as an executor. It drives recovery.
func (l *Liveness) LastRequest() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastRequest, l.requested
}
