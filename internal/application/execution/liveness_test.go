package execution_test

import (
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

var livenessStart = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func TestLivenessStartsAtTheStartTimeWithNoRequest(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	if got := l.LastSeen(); !got.Equal(livenessStart) {
		t.Errorf("LastSeen = %v, want the start time %v", got, livenessStart)
	}
	if got, ok := l.LastRequest(); ok {
		t.Errorf("LastRequest = %v, true; want it absent until an executor is seen", got)
	}
}

func TestLivenessSawMovesBothTimes(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	at := livenessStart.Add(7 * time.Minute)
	l.Saw(at)
	if got := l.LastSeen(); !got.Equal(at) {
		t.Errorf("LastSeen = %v, want %v", got, at)
	}
	if got, ok := l.LastRequest(); !ok || !got.Equal(at) {
		t.Errorf("LastRequest = %v, %v; want %v, true", got, ok, at)
	}
}

func TestLivenessKeepsTheLatestTime(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	late, early := livenessStart.Add(9*time.Minute), livenessStart.Add(8*time.Minute)
	l.Saw(late)
	l.Saw(early) // a handler that took its time before another, but reached the record after it
	if got := l.LastSeen(); !got.Equal(late) {
		t.Errorf("LastSeen = %v after an earlier Saw, want %v", got, late)
	}
	if got, ok := l.LastRequest(); !ok || !got.Equal(late) {
		t.Errorf("LastRequest = %v, %v after an earlier Saw; want %v, true", got, ok, late)
	}
}

func TestLivenessIsSafeForConcurrentUse(t *testing.T) {
	const handlers = 50
	l := execution.NewLiveness(livenessStart)
	var wg sync.WaitGroup
	for i := 1; i <= handlers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Saw(livenessStart.Add(time.Duration(i) * time.Second))
			l.LastSeen()
			l.LastRequest()
		}()
	}
	wg.Wait()
	last := livenessStart.Add(handlers * time.Second)
	if got := l.LastSeen(); !got.Equal(last) {
		t.Errorf("LastSeen = %v, want the latest %v", got, last)
	}
	if got, ok := l.LastRequest(); !ok || !got.Equal(last) {
		t.Errorf("LastRequest = %v, %v; want %v, true", got, ok, last)
	}
}

func TestLivenessIsSilentFromThirtySecondsAfterTheLastSight(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	for name, c := range map[string]struct {
		now  time.Duration
		want bool
	}{
		"just seen":                   {0, false},
		"a moment before 30 s":        {29*time.Second + 999*time.Millisecond, false},
		"exactly 30 s":                {30 * time.Second, true},
		"a minute after":              {time.Minute, true},
		"before the time it was seen": {-time.Hour, false},
	} {
		if got := l.Silent(livenessStart.Add(c.now)); got != c.want {
			t.Errorf("%s: Silent = %v, want %v", name, got, c.want)
		}
	}
}

func TestLivenessIsNotSilentAfterAnExecutorWasSeen(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	seen := livenessStart.Add(10 * time.Minute)
	l.Saw(seen)
	if l.Silent(seen.Add(29 * time.Second)) {
		t.Error("silent 29 s after the last request")
	}
	if !l.Silent(seen.Add(30 * time.Second)) {
		t.Error("not silent 30 s after the last request")
	}
}

func TestLivenessHeardSinceNeedsARealRequestAfterTheTime(t *testing.T) {
	l := execution.NewLiveness(livenessStart)
	ended := livenessStart.Add(time.Minute)
	if l.HeardSince(ended) || l.HeardSince(livenessStart.Add(-time.Hour)) {
		t.Error("a restart alone counts as an executor request")
	}
	l.Saw(ended)
	if l.HeardSince(ended) {
		t.Error("a request at the very time counts as later")
	}
	l.Saw(ended.Add(time.Second))
	if !l.HeardSince(ended) || l.HeardSince(ended.Add(time.Second)) {
		t.Error("HeardSince does not compare the last request with the time")
	}
}
