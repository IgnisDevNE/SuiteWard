package run

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

const libTests = "package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n\nfunc TestB(t *testing.T) {}\n"

// aJob is a canonical job with two declared tests, and the runtime that passes them.
func aJob(id int64) (*served, *fakeRuntime) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	return s.serve(id), newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, func(string, string) string { return "pass" }), nil
	})
}

func TestAnEmptyLeaseIsFollowedByAWaitOfFiveSeconds(t *testing.T) {
	svc := newService(t)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.immediate, d.sleeps.reached = 3, make(chan struct{}) })
	wait(d, d.sleeps.reached)
	if got := d.sleeps.durations(); !slices.Equal(got[:3], []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second}) {
		t.Errorf("the waits after a 204 were %v; want 5 s each", got)
	}
	if n := svc.leaseRequests(); n < 4 {
		t.Errorf("%d lease requests; the executor asks again after each wait", n)
	}
}

// 502 is the answer when GitHub could not be read for the job; a refused token, a service that is not ready, a failure and an answer that
// breaks the contract are errors too. None of them is a reason to ask faster.
func TestAfterAnErrorTheLeaseWaitDoublesUpToSixtySecondsAndStartsAgain(t *testing.T) {
	broken := &served{job: client.Job{JobID: 99}} // a job that fails the contract's checks: it has no lease
	svc := newService(t, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusInternalServerError,
		http.StatusBadGateway, broken, http.StatusBadGateway, nil, http.StatusBadGateway, nil)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.immediate, d.sleeps.reached = 10, make(chan struct{}) })
	wait(d, d.sleeps.reached)
	want := []time.Duration{5, 10, 20, 40, 60, 60, 60, 5, 5, 5} // seconds: the 204s wait 5 and an answer starts the backoff again
	for i := range want {
		want[i] *= time.Second
	}
	if got := d.sleeps.durations(); !slices.Equal(got[:10], want) {
		t.Errorf("the waits were %v; want %v", got[:10], want)
	}
}

func TestAHeartbeatGoesAboutEveryTwentySecondsWhileTheJobRuns(t *testing.T) {
	sv, rt := aJob(1)
	beats := make(chan int, 8)
	sv.beat = func(n int) int { beats <- n; return http.StatusOK }
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	d := start(t, newService(t, sv), rt)
	wait(d, rt.entered)
	if every := wait(d, d.tickFor); every != 20*time.Second {
		t.Errorf("the heartbeat ticks every %v; want 20 s", every)
	}
	d.tick()
	d.tick()
	if a, b := wait(d, beats), wait(d, beats); a != 1 || b != 2 {
		t.Errorf("heartbeats %d and %d; want 1 and 2", a, b)
	}
	rt.gate <- struct{}{}
	if p := d.result(); p.lease != sv.job.LeaseID {
		t.Errorf("the result carries the lease %q; want %q", p.lease, sv.job.LeaseID)
	}
}

func TestAConflictOnAHeartbeatStopsTheWorkAndPostsNothing(t *testing.T) {
	for name, status := range map[string]int{"the lease was lost": http.StatusConflict, "the job is unknown": http.StatusNotFound} {
		sv, rt := aJob(1)
		sv.beat = func(int) int { return status }
		rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
		svc := newService(t, sv)
		d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
		wait(d, rt.entered)
		d.tick()
		wait(d, d.sleeps.reached) // the work was dropped, and the executor asked for the next job, which was a 204
		d.noResult()
		if svc.leaseRequests() < 2 {
			t.Errorf("%s: the executor did not ask for another job", name)
		}
	}
}

func TestAHeartbeatThatFailsOtherwiseDoesNotStopTheWork(t *testing.T) {
	sv, rt := aJob(1)
	sv.beat = func(int) int { return http.StatusInternalServerError }
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	d := start(t, newService(t, sv), rt)
	wait(d, rt.entered)
	d.tick()
	rt.gate <- struct{}{}
	d.result()
}

func TestAJobPastItsDeadlineIsDroppedWithoutAResult(t *testing.T) {
	s := canonicalScenario()
	s.deadline = time.Now().Add(-time.Minute)
	svc := newService(t, s.serve(1))
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.noResult()
}

func TestTheResultIsPostedOnceWhateverTheServiceAnswers(t *testing.T) {
	for name, answer := range map[string]func(int) (int, string){
		"pending":  func(int) (int, string) { return http.StatusOK, client.StatePending },
		"conflict": func(int) (int, string) { return http.StatusConflict, "" },
		"failure":  func(int) (int, string) { return http.StatusInternalServerError, "" },
		"refusal":  func(int) (int, string) { return http.StatusBadRequest, "" },
	} {
		sv, rt := aJob(1)
		sv.answer = answer
		svc := newService(t, sv)
		d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
		d.result()
		wait(d, d.sleeps.reached) // the executor went on to ask for a job
		if n := sv.results.Load(); n != 1 {
			t.Errorf("%s: the result was posted %d times; want once", name, n)
		}
	}
}

func TestAfterAPendingAnswerTheExecutorLeasesTheNextJob(t *testing.T) {
	first, rt := aJob(1)
	first.answer = func(int) (int, string) { return http.StatusOK, client.StatePending }
	second, _ := aJob(2)
	d := start(t, newService(t, first, second), rt)
	if got := d.result().id; got != 1 {
		t.Errorf("the first result is of job %d", got)
	}
	if got := d.result().id; got != 2 {
		t.Errorf("the second result is of job %d", got)
	}
}

func TestOnShutdownTheExecutorLeasesNoMoreAndFinishesTheJobInFlight(t *testing.T) {
	sv, rt := aJob(1)
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	svc := newService(t, sv, nil, nil)
	d := start(t, svc, rt)
	wait(d, rt.entered)
	d.cancel()
	rt.gate <- struct{}{} // the job in flight is still allowed to end, within the grace
	d.result()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a clean stop; want nil", err)
	}
	if n := svc.leaseRequests(); n != 1 {
		t.Errorf("%d lease requests; the executor stops leasing at the signal", n)
	}
	if got := d.sleeps.durations(); !slices.Equal(got, []time.Duration{30 * time.Second}) {
		t.Errorf("the executor waited %v; the one wait is the 30 s grace of the job in flight", got)
	}
}

// The grace is 30 s from the signal. When it is over the work is dropped (the sandbox kills the container with its context), nothing is
// posted, the executor exits 0, and the lease expires, so that the job is leased again. The test's sleeper ends the grace at once.
func TestAJobInFlightIsAbandonedWhenTheGraceAfterTheSignalIsOver(t *testing.T) {
	sv, rt := aJob(1)
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4) // the gate is never opened: the job would run for ever
	svc := newService(t, sv, nil, nil)
	d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.immediate, d.patience = 1, 5*time.Second })
	wait(d, rt.entered)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after the grace; want a clean stop", err)
	}
	d.noResult()
	if n := sv.results.Load(); n != 0 {
		t.Errorf("%d results were posted by a job that was abandoned", n)
	}
	if got := d.sleeps.durations(); !slices.Equal(got, []time.Duration{30 * time.Second}) {
		t.Errorf("the executor waited %v; the one wait is the 30 s grace", got)
	}
	if n := svc.leaseRequests(); n != 1 {
		t.Errorf("%d lease requests; the executor leases nothing after the signal", n)
	}
}

func TestShutdownWhileWaitingStopsAtOnce(t *testing.T) {
	d := start(t, newService(t), newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a clean stop; want nil", err)
	}
}

func TestWithoutARuntimeTheExecutorDoesNotStart(t *testing.T) {
	rt := newRuntime(nil)
	rt.infoErr = errors.New("the engine does not answer")
	svc := newService(t, nil)
	d := start(t, svc, rt)
	if err := d.stopped(); err == nil || !strings.Contains(err.Error(), "container runtime") {
		t.Errorf("Run = %v with no container runtime; want a fatal error that says so", err)
	}
	if n := svc.leaseRequests(); n != 0 {
		t.Errorf("%d lease requests without a runtime", n)
	}
}

func TestLeftoverContainersAreSweptOnceBeforeTheFirstLease(t *testing.T) {
	svc := newService(t)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) {
		d.sleeps.reached = make(chan struct{})
	})
	wait(d, d.sleeps.reached)
	if n := d.swept.Load(); n != 1 {
		t.Errorf("the sweep ran %d times; want once, before the first lease", n)
	}
}

func TestAFailedSweepIsLoggedAndTheExecutorGoesOn(t *testing.T) {
	d := start(t, newService(t), newRuntime(nil), func(cfg *Config, d *driver) {
		d.sleeps.reached = make(chan struct{})
		cfg.Sweep = func(context.Context) error { return errors.New("the engine would not list") }
	})
	wait(d, d.sleeps.reached) // fails the test when the executor stopped after the failed sweep
}

func TestNewChecksWhatItIsGiven(t *testing.T) {
	api, err := client.New("http://127.0.0.1:1", testToken)
	if err != nil {
		t.Fatal(err)
	}
	good := Config{API: api, Runtime: newRuntime(nil), Sweep: func(context.Context) error { return nil }, Version: "1.2.3", BinarySHA256: testBinary, Log: discardLog()}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good) = %v", err)
	}
	for name, change := range map[string]func(*Config){
		"no API":                  func(c *Config) { c.API = nil },
		"no runtime":              func(c *Config) { c.Runtime = nil },
		"no sweep":                func(c *Config) { c.Sweep = nil },
		"no logger":               func(c *Config) { c.Log = nil },
		"a version of no shape":   func(c *Config) { c.Version = "not a version" },
		"no version":              func(c *Config) { c.Version = "" },
		"a binary digest in caps": func(c *Config) { c.BinarySHA256 = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB" },
		"a short binary digest":   func(c *Config) { c.BinarySHA256 = "abab" },
	} {
		cfg := good
		change(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted the configuration", name)
		}
	}
}

func TestTheDefaultsReadNoProxySettingAndFollowTheContract(t *testing.T) {
	api, err := client.New("http://127.0.0.1:1", testToken)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{API: api, Runtime: newRuntime(nil), Sweep: func(context.Context) error { return nil }, Version: "1.2.3", BinarySHA256: testBinary, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if e.cfg.Pacing != DefaultPacing || e.cfg.Proxy.Base != "https://proxy.golang.org" {
		t.Errorf("the defaults are %+v and %q", e.cfg.Pacing, e.cfg.Proxy.Base)
	}
	if e.cfg.Proxy.Client == nil {
		t.Fatal("no default client for the module proxy")
	}
	transport, ok := e.cfg.Proxy.Client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Errorf("the prefetch client is %T and has a proxy function: the egress goes direct, and no environment variable moves it", e.cfg.Proxy.Client.Transport)
	}
	if DefaultPacing != (Pacing{Idle: 5 * time.Second, Backoff: 5 * time.Second, MaxBackoff: time.Minute, Heartbeat: 20 * time.Second, Grace: 30 * time.Second}) {
		t.Errorf("DefaultPacing = %+v", DefaultPacing)
	}
}
