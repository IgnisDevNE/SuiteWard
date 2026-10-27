package run

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

// A service that restarts answers 503 while it stops (and does not answer at all while it is down). The result is finished work, and losing
// it costs an attempt, so the executor sends the same body again after a wait, and stops when the service has taken it.
func TestAResultTheServiceDidNotTakeIsPostedAgainUntilItIsTaken(t *testing.T) {
	for name, firstAnswer := range map[string]int{
		"a 503, the service is stopping": http.StatusServiceUnavailable,
		"no answer, the service is down": dropConnection,
	} {
		t.Run(name, func(t *testing.T) {
			sv, rt := aJob(1)
			sv.answer = func(n int) (int, string) {
				switch n {
				case 1:
					return firstAnswer, ""
				case 2:
					return http.StatusServiceUnavailable, "" // the service is still not ready
				}
				return http.StatusOK, ""
			}
			d := start(t, newService(t, sv), rt, func(_ *Config, d *driver) {
				d.sleeps.immediate, d.sleeps.reached, d.patience = 2, make(chan struct{}), 5*time.Second
			})
			p1, p2, p3 := d.result(), d.result(), d.result()
			if !bytes.Equal(p1.body, p2.body) || !bytes.Equal(p1.body, p3.body) {
				t.Errorf("the result was sent again with another body:\n%s\n%s\n%s", p1.body, p2.body, p3.body)
			}
			wait(d, d.sleeps.reached) // the service took the third post, and the executor went on to ask for a job
			if n := sv.results.Load(); n != 3 {
				t.Errorf("the result was posted %d times; want 3: two that were not taken, and the one that was", n)
			}
			var idle []time.Duration
			for _, w := range d.sleeps.durations() {
				if waitKindOf(w) == idleWait {
					idle = append(idle, w)
				}
			}
			if len(idle) < 2 || idle[0] != 5*time.Second || idle[1] != 10*time.Second {
				t.Errorf("the executor waited %v after the refusals; want 5 s and then 10 s (the lease backoff, doubling)", idle)
			}
		})
	}
}

// The attempt ends at its deadline less the margin: a service that refuses until then is given up, as a result that was never posted is.
func TestAResultTheServiceRefusesUntilTheMarginIsGivenUp(t *testing.T) {
	sv, rt := aJob(1)
	sv.answer = func(int) (int, string) { return http.StatusServiceUnavailable, "" }
	logs := newLogRecorder()
	fire := make(chan struct{})
	d := start(t, newService(t, sv), rt, gate(deadlineWait, fire), func(cfg *Config, d *driver) {
		cfg.Log = slog.New(logs)
		d.sleeps.immediate, d.sleeps.reached, d.patience = 2, make(chan struct{}), 10*time.Second
	})
	wait(d, d.sleeps.reached) // the third wait for a new post is the one that blocks: the result was posted three times by now
	for range 3 {
		d.result()
	}
	close(fire) // the deadline less the margin has come
	for gaveUp := false; !gaveUp; {
		gaveUp = wait(d, logs.records).msg == "the result was not accepted: its lease will expire"
	}
	d.noResult()
	if n := sv.results.Load(); n != 3 {
		t.Errorf("the result was posted %d times; want 3: none after the margin", n)
	}
}
