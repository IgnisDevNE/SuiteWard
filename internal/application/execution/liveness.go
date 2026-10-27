package execution

import "time"

// Liveness is the in-memory record of when an executor was last seen (compiling stub).
type Liveness struct{}

// NewLiveness starts the record at start.
func NewLiveness(start time.Time) *Liveness { return &Liveness{} }

// Saw records that a lease request or a heartbeat arrived at at.
func (l *Liveness) Saw(at time.Time) {}

// LastSeen is the time an executor was last seen, the start time until the first Saw.
func (l *Liveness) LastSeen() time.Time { return time.Time{} }

// LastRequest is the time of the last lease request or heartbeat, absent until the first Saw.
func (l *Liveness) LastRequest() (time.Time, bool) { return time.Time{}, false }
