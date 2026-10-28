package execution

import (
	"sync"
	"time"

	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// Liveness is the service's in-memory record of when an executor was last seen, from its lease requests (answered with a job, 204 or
// 502), its heartbeats and its result posts (docs/contracts/runtime.md, "The executor is seen, not asked"). It keeps nothing across a restart, and
// it never moves back: a time earlier than the one held is ignored, so concurrent handlers cannot make an executor look older.
type Liveness struct {
	mu          sync.Mutex
	lastSeen    time.Time
	lastRequest time.Time
	requested   bool
}

// NewLiveness starts the record at start: the service has just started, so it does not see a missing executor at once.
func NewLiveness(start time.Time) *Liveness { return &Liveness{lastSeen: start} }

// Saw records that a lease request, a heartbeat or a result post arrived at at.
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

// Silent reports whether no executor was seen for NotRespondingAfter at now: the runner does not respond. Startup counts as seen, so a service
// that has just started is not silent in its first 30 s.
func (l *Liveness) Silent(now time.Time) bool {
	return now.Sub(l.LastSeen()) >= domain.NotRespondingAfter
}

// HeardSince reports whether a real executor request arrived after t, compared at the precision that is stored: a lease request, a heartbeat or a
// result post. A restart of the service alone is none.
func (l *Liveness) HeardSince(t time.Time) bool {
	last, requested := l.LastRequest()
	return requested && Stored(last).After(Stored(t))
}
